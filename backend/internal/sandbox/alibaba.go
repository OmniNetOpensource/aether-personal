package sandbox

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// Alibaba uses the E2B REST control plane and envd Connect JSON protocol.
// Management credentials are never passed to the sandbox or its desktop.
type Alibaba struct {
	control *Client
	domain  string
}

type alibabaEnvironment struct {
	ID       string            `json:"sandboxID"`
	State    string            `json:"state"`
	Token    string            `json:"envdAccessToken"`
	Domain   string            `json:"domain"`
	EndAt    time.Time         `json:"endAt"`
	Metadata map[string]string `json:"metadata"`
}

//go:embed alibaba-init.sh
var alibabaInit string

//go:embed alibaba-command.py
var alibabaCommand string

func (a *Alibaba) Create(ctx context.Context, userID, allocationID uuid.UUID, template string) (Environment, error) {
	var result alibabaEnvironment
	err := a.control.json(ctx, http.MethodPost, "/sandboxes", map[string]any{
		"templateID": template, "timeout": 600, "secure": true, "allowInternetAccess": true,
		"autoPause": true, "autoResume": map[string]bool{"enabled": false},
		"metadata": map[string]string{"aether_user_id": userID.String(), "aether_allocation_id": allocationID.String()},
	}, &result)
	if err != nil {
		return Environment{}, err
	}
	return a.environment(result)
}

func (a *Alibaba) environment(e alibabaEnvironment) (Environment, error) {
	if !strings.HasPrefix(e.ID, "sbx-") || strings.Trim(e.ID, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return Environment{}, errors.New("invalid Alibaba sandbox ID")
	}
	if e.State == "" {
		e.State = "running"
	}
	return Environment{ID: "aliyun:" + e.ID, State: strings.ToLower(e.State)}, nil
}

func alibabaPath(id string) (string, error) {
	if !strings.HasPrefix(id, "aliyun:sbx-") {
		return "", errors.New("sandbox belongs to another provider; existing mapping preserved")
	}
	raw := strings.TrimPrefix(id, "aliyun:")
	if strings.Trim(raw, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return "", errors.New("invalid Alibaba sandbox ID")
	}
	return "/sandboxes/" + raw, nil
}

func (a *Alibaba) info(ctx context.Context, id string) (alibabaEnvironment, error) {
	p, err := alibabaPath(id)
	if err != nil {
		return alibabaEnvironment{}, err
	}
	var e alibabaEnvironment
	err = a.control.json(ctx, http.MethodGet, p, nil, &e)
	if err != nil {
		return e, err
	}
	if "aliyun:"+e.ID != id || e.Domain != a.domain {
		return e, errors.New("Alibaba sandbox identity or regional domain mismatch")
	}
	return e, nil
}

func (a *Alibaba) Get(ctx context.Context, id string) (Environment, error) {
	e, err := a.info(ctx, id)
	if err != nil {
		return Environment{}, err
	}
	return a.environment(e)
}

func (a *Alibaba) Find(ctx context.Context, allocationID uuid.UUID) ([]Environment, error) {
	// Alibaba's E2B compatibility endpoint returns the complete running/paused list.
	query := url.Values{"metadata": {url.Values{"aether_allocation_id": {allocationID.String()}}.Encode()}, "state": {"running,paused"}}
	var items []alibabaEnvironment
	if err := a.control.json(ctx, http.MethodGet, "/sandboxes?"+query.Encode(), nil, &items); err != nil {
		return nil, err
	}
	var result []Environment
	for _, item := range items {
		if item.Metadata["aether_allocation_id"] != allocationID.String() {
			continue
		}
		e, err := a.environment(item)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, nil
}

func (a *Alibaba) Pause(ctx context.Context, id string) error {
	p, err := alibabaPath(id)
	if err != nil {
		return err
	}
	return a.control.json(ctx, http.MethodPost, p+"/pause", nil, nil)
}

func (a *Alibaba) Resume(ctx context.Context, id string) error {
	p, err := alibabaPath(id)
	if err != nil {
		return err
	}
	return a.control.json(ctx, http.MethodPost, p+"/connect", map[string]int{"timeout": 600}, nil)
}

func (a *Alibaba) Ping(ctx context.Context, id string) error {
	// Bootstrap is locked and runs once; resume never launches a second Chrome.
	_, err := a.exec(ctx, id, alibabaInit, true)
	return err
}

func (a *Alibaba) StartCommand(ctx context.Context, id, command, directory string, timeout time.Duration) (string, error) {
	e, err := a.info(ctx, id)
	if err != nil {
		return "", err
	}
	// Keep the provider's fallback pause beyond this command's enforced deadline.
	seconds := max(600, int(timeout.Seconds())+60)
	if time.Until(e.EndAt) < time.Duration(seconds)*time.Second {
		p, _ := alibabaPath(id)
		if err := a.control.json(ctx, http.MethodPost, p+"/timeout", map[string]int{"timeout": seconds}, nil); err != nil {
			return "", err
		}
	}
	commandID := uuid.NewString()
	payload, _ := json.Marshal(map[string]any{"id": commandID, "command": command, "directory": directory, "timeout": timeout.Seconds()})
	script := "python3 -c " + shellQuote(alibabaCommand) + " " + shellQuote(base64.StdEncoding.EncodeToString(payload))
	_, err = a.exec(ctx, id, script, false)
	return commandID, err
}

func commandDirectory(commandID string) (string, error) {
	if _, err := uuid.Parse(commandID); err != nil {
		return "", errors.New("invalid command ID")
	}
	return "/var/lib/aether/commands/" + commandID, nil
}

func (a *Alibaba) Command(ctx context.Context, id, commandID string) (Command, error) {
	directory, err := commandDirectory(commandID)
	if err != nil {
		return Command{}, err
	}
	// Query the live supervisor as well as its durable receipt. An absent receipt
	// or a dead supervisor with no exit code is an unknown result, never success.
	script := `import json,pathlib,os
p=pathlib.Path(` + fmt.Sprintf("%q", directory) + `)
d=json.loads((p/'status.json').read_text())
if d['running']:
 try:
  stat=pathlib.Path('/proc/'+str(d['supervisor'])+'/stat').read_text().rsplit(')',1)[1].split()
  assert stat[19]==d['start_time'] and stat[0]!='Z'
 except (OSError,AssertionError):
  raise RuntimeError('command supervisor disappeared; result unknown')
print(json.dumps(d))`
	out, err := a.exec(ctx, id, "python3 -c "+shellQuote(script), true)
	if err != nil {
		return Command{}, err
	}
	var result Command
	if json.Unmarshal([]byte(out), &result) != nil || result.ID != commandID || (!result.Running && result.ExitCode == nil) {
		return Command{}, errors.New("invalid Alibaba command receipt")
	}
	return result, nil
}

func (a *Alibaba) Logs(ctx context.Context, id, commandID string) (string, error) {
	directory, err := commandDirectory(commandID)
	if err != nil {
		return "", err
	}
	data, err := a.ReadFile(ctx, id, directory+"/output.log")
	return string(data), err
}

func (a *Alibaba) StopCommand(ctx context.Context, id, commandID string) error {
	directory, err := commandDirectory(commandID)
	if err != nil {
		return err
	}
	script := `import json,pathlib,os,signal
p=pathlib.Path(` + fmt.Sprintf("%q", directory) + `)
d=json.loads((p/'status.json').read_text())
if d['running']:
 try:
  stat=pathlib.Path('/proc/'+str(d['pid'])+'/stat').read_text().rsplit(')',1)[1].split()
  if stat[19]==d['child_start_time']: os.killpg(d['pid'],signal.SIGKILL)
 except ProcessLookupError: pass
 except FileNotFoundError: pass`
	_, err = a.exec(ctx, id, "python3 -c "+shellQuote(script), true)
	return err
}

func (a *Alibaba) envd(ctx context.Context, id string) (string, string, error) {
	e, err := a.info(ctx, id)
	if err != nil {
		return "", "", err
	}
	if e.State != "running" || e.Token == "" {
		return "", "", errors.New("Alibaba sandbox execution service is not running")
	}
	return "https://49983-" + e.ID + "." + a.domain, e.Token, nil
}

func (a *Alibaba) envdRequest(ctx context.Context, id, method, requestPath string, body io.Reader, contentType string) (*http.Response, error) {
	base, token, err := a.envd(ctx, id)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, base+requestPath, body)
	if err != nil {
		return nil, errors.New("invalid envd request")
	}
	request.Header.Set("X-Access-Token", token)
	request.SetBasicAuth("root", "")
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Connect-Protocol-Version", "1")
	response, err := a.control.httpClient.Do(request)
	if err != nil {
		return nil, errors.New("Alibaba execution request failed; operation may already have happened")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, fmt.Errorf("Alibaba execution service returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func (a *Alibaba) ReadFile(ctx context.Context, id, filePath string) ([]byte, error) {
	r, err := a.envdRequest(ctx, id, http.MethodGet, "/files?"+url.Values{"path": {filePath}, "username": {"root"}}.Encode(), nil, "")
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("Alibaba file read failed")
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("sandbox file exceeds 2 MiB read limit")
	}
	return data, nil
}

func (a *Alibaba) WriteFile(ctx context.Context, id, filePath string, content []byte) error {
	if len(content) > maxResponseBytes {
		return errors.New("sandbox file exceeds 2 MiB write limit")
	}
	r, err := a.envdRequest(ctx, id, http.MethodPost, "/files?"+url.Values{"path": {filePath}, "username": {"root"}}.Encode(), bytes.NewReader(content), "application/octet-stream")
	if err != nil {
		return err
	}
	return r.Body.Close()
}

func (a *Alibaba) exec(ctx context.Context, id, command string, wait bool) (string, error) {
	payload, _ := json.Marshal(map[string]any{"process": map[string]any{"cmd": "/bin/bash", "args": []string{"-c", command}, "envs": map[string]string{"DISPLAY": ":0", "HOME": "/root"}, "cwd": "/tmp"}})
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	r, err := a.envdRequest(ctx, id, http.MethodPost, "/process.Process/Start", bytes.NewReader(frame), "application/connect+json")
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	var output strings.Builder
	for frames := 0; frames < 10000; frames++ {
		var header [5]byte
		if _, err := io.ReadFull(r.Body, header[:]); err != nil {
			return "", errors.New("Alibaba command stream ended without a receipt; result unknown")
		}
		size := binary.BigEndian.Uint32(header[1:])
		if size > maxResponseBytes {
			return "", errors.New("Alibaba command frame exceeds limit")
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(r.Body, data); err != nil {
			return "", errors.New("Alibaba command frame incomplete; result unknown")
		}
		if header[0] != 0 {
			return "", errors.New("Alibaba command stream returned an error; result unknown")
		}
		var response struct {
			Event struct {
				Start *struct {
					PID int `json:"pid"`
				} `json:"start"`
				Data *struct {
					Stdout []byte `json:"stdout"`
					Stderr []byte `json:"stderr"`
				} `json:"data"`
				End *struct {
					ExitCode int    `json:"exitCode"`
					Exited   bool   `json:"exited"`
					Error    string `json:"error"`
				} `json:"end"`
			} `json:"event"`
		}
		if json.Unmarshal(data, &response) != nil {
			return "", errors.New("invalid Alibaba command event")
		}
		if response.Event.Start != nil && response.Event.Start.PID > 0 && !wait {
			return "", nil
		}
		if e := response.Event.Data; e != nil {
			if output.Len()+len(e.Stdout)+len(e.Stderr) > maxResponseBytes {
				return "", errors.New("Alibaba command output exceeds limit")
			}
			output.Write(e.Stdout)
			output.Write(e.Stderr)
		}
		if e := response.Event.End; e != nil {
			if !e.Exited || e.ExitCode != 0 || e.Error != "" {
				return "", fmt.Errorf("Alibaba command failed with exit code %d", e.ExitCode)
			}
			return output.String(), nil
		}
	}
	return "", errors.New("too many Alibaba command events")
}

func (a *Alibaba) OpenDesktop(ctx context.Context, id string) (net.Conn, error) {
	// No connect/resume call on the observation path; autoResume is disabled at creation.
	e, err := a.info(ctx, id)
	if err != nil {
		return nil, err
	}
	if e.State != "running" {
		return nil, errors.New("desktop is paused")
	}
	secret, err := a.ReadFile(ctx, id, "/var/lib/aether/desktop-secret")
	if err != nil {
		return nil, err
	}
	client := *a.control.httpClient
	client.Timeout = 0
	dialContext, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(dialContext, "wss://6080-"+e.ID+"."+a.domain+"/websockify", &websocket.DialOptions{
		HTTPClient: &client, HTTPHeader: http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("aether:"+strings.TrimSpace(string(secret))))}}, Subprotocols: []string{"binary"},
	})
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, errors.New("Alibaba desktop connection failed")
	}
	return websocket.NetConn(ctx, connection, websocket.MessageBinary), nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
