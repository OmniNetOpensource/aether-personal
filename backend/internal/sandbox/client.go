package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("sandbox provider resource not found")

const (
	maxResponseBytes = 2 << 20
	maxLogBytes      = 32 << 10
)

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

type Environment struct {
	ID    string
	State string
}

type Command struct {
	ID       string `json:"id"`
	Running  bool   `json:"running"`
	ExitCode *int   `json:"exit_code"`
}

type environmentResponse struct {
	ID     string `json:"id"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
}

func NewClient(baseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	client := *httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/") + "/v1", apiKey: apiKey, httpClient: &client}
}

func (c *Client) Create(ctx context.Context, userID, allocationID uuid.UUID, image string) (Environment, error) {
	body := map[string]any{
		"image":          map[string]string{"uri": image},
		"timeout":        nil,
		"resourceLimits": map[string]string{"cpu": "2", "memory": "2Gi"},
		"entrypoint":     []string{"/usr/local/bin/start-desktop"},
		"metadata":       map[string]string{"aether_user_id": userID.String(), "aether_allocation_id": allocationID.String()},
	}
	var response environmentResponse
	if err := c.json(ctx, http.MethodPost, "/sandboxes", body, &response); err != nil {
		return Environment{}, err
	}
	return decodeEnvironment(response)
}

func (c *Client) Find(ctx context.Context, allocationID uuid.UUID) ([]Environment, error) {
	metadata := url.Values{"aether_allocation_id": {allocationID.String()}}.Encode()
	var environments []Environment
	for page := 1; ; page++ {
		query := url.Values{"metadata": {metadata}, "page": {strconv.Itoa(page)}, "pageSize": {"200"}}
		var response struct {
			Items      []environmentResponse `json:"items"`
			Pagination struct {
				HasNextPage bool `json:"hasNextPage"`
			} `json:"pagination"`
		}
		if err := c.json(ctx, http.MethodGet, "/sandboxes?"+query.Encode(), nil, &response); err != nil {
			return nil, err
		}
		for _, item := range response.Items {
			environment, err := decodeEnvironment(item)
			if err != nil {
				return nil, err
			}
			environments = append(environments, environment)
		}
		if !response.Pagination.HasNextPage {
			return environments, nil
		}
		if len(response.Items) == 0 || page >= 100 {
			return nil, errors.New("sandbox provider returned invalid pagination")
		}
	}
}

func (c *Client) Get(ctx context.Context, id string) (Environment, error) {
	var response environmentResponse
	if err := c.json(ctx, http.MethodGet, sandboxPath(id), nil, &response); err != nil {
		return Environment{}, err
	}
	return decodeEnvironment(response)
}

func (c *Client) Pause(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodPost, sandboxPath(id)+"/pause", nil, nil)
}

func (c *Client) Resume(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodPost, sandboxPath(id)+"/resume", nil, nil)
}

func (c *Client) Ping(ctx context.Context, id string) error {
	return c.json(ctx, http.MethodGet, execdPath(id)+"/ping", nil, nil)
}

func (c *Client) StartCommand(ctx context.Context, id, command, directory string, timeout time.Duration) (string, error) {
	body, err := json.Marshal(map[string]any{
		"command": command, "cwd": directory, "timeout": timeout.Milliseconds(), "background": true,
	})
	if err != nil {
		return "", err
	}
	response, err := c.request(ctx, http.MethodPost, execdPath(id)+"/command", bytes.NewReader(body), "application/json")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(io.LimitReader(response.Body, maxResponseBytes+1))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return "", errors.New("sandbox provider returned invalid command event")
		}
		if event.Type == "init" && event.Text != "" {
			// Closing this background stream stops waiting, not the remote process.
			return event.Text, nil
		}
		if event.Type == "error" {
			return "", errors.New("sandbox provider could not start command; execution state may be unknown")
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return "", errors.New("sandbox provider returned no command ID; execution state may be unknown")
}

func (c *Client) Command(ctx context.Context, id, commandID string) (Command, error) {
	var command Command
	if err := c.json(ctx, http.MethodGet, execdPath(id)+"/command/status/"+url.PathEscape(commandID), nil, &command); err != nil {
		return Command{}, err
	}
	if command.ID == "" {
		return Command{}, errors.New("sandbox provider returned invalid command status")
	}
	return command, nil
}

func (c *Client) Logs(ctx context.Context, id, commandID string) (string, error) {
	response, err := c.request(ctx, http.MethodGet, execdPath(id)+"/command/"+url.PathEscape(commandID)+"/logs", nil, "")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, maxLogBytes+1))
	if err != nil {
		return "", errors.New("sandbox provider log read failed")
	}
	if len(content) > maxLogBytes {
		const marker = "\n[output truncated]"
		content = append(content[:maxLogBytes-len(marker)], marker...)
	}
	return string(content), nil
}

func (c *Client) StopCommand(ctx context.Context, id, commandID string) error {
	return c.json(ctx, http.MethodDelete, execdPath(id)+"/command?"+url.Values{"id": {commandID}}.Encode(), nil, nil)
}

func (c *Client) ReadFile(ctx context.Context, id, filePath string) ([]byte, error) {
	response, err := c.request(ctx, http.MethodGet, execdPath(id)+"/files/download?"+url.Values{"path": {filePath}}.Encode(), nil, "")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("sandbox provider file read failed")
	}
	if len(content) > maxResponseBytes {
		return nil, errors.New("sandbox file exceeds 2 MiB read limit")
	}
	return content, nil
}

func (c *Client) WriteFile(ctx context.Context, id, filePath string, content []byte) error {
	if len(content) > maxResponseBytes {
		return errors.New("sandbox file exceeds 2 MiB write limit")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata, err := json.Marshal(map[string]any{"path": filePath, "mode": 644})
	if err != nil {
		return err
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="metadata"; filename="metadata"`)
	header.Set("Content-Type", "application/json")
	metadataPart, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := metadataPart.Write(metadata); err != nil {
		return err
	}
	filePart, err := writer.CreateFormFile("file", path.Base(filePath))
	if err != nil {
		return err
	}
	if _, err := filePart.Write(content); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	response, err := c.request(ctx, http.MethodPost, execdPath(id)+"/files/upload", &body, writer.FormDataContentType())
	if err != nil {
		return err
	}
	return response.Body.Close()
}

func (c *Client) json(ctx context.Context, method, requestPath string, value, result any) error {
	var body io.Reader
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	response, err := c.request(ctx, method, requestPath, body, "application/json")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if result == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return errors.New("sandbox provider response read failed")
	}
	if len(data) > maxResponseBytes || json.Unmarshal(data, result) != nil {
		return errors.New("sandbox provider returned invalid JSON response")
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, requestPath string, body io.Reader, contentType string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+requestPath, body)
	if err != nil {
		return nil, errors.New("invalid sandbox provider request")
	}
	request.Header.Set("OPEN-SANDBOX-API-KEY", c.apiKey)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("sandbox provider request failed; submitted operations may still be running")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		if response.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("sandbox provider returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func decodeEnvironment(response environmentResponse) (Environment, error) {
	if response.ID == "" || response.Status.State == "" {
		return Environment{}, errors.New("sandbox provider returned invalid environment")
	}
	return Environment{ID: response.ID, State: strings.ToLower(response.Status.State)}, nil
}

func sandboxPath(id string) string { return "/sandboxes/" + url.PathEscape(id) }
func execdPath(id string) string   { return sandboxPath(id) + "/proxy/44772" }
