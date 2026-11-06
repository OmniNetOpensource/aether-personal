// 本命令用隔离数据库、真实单后端和受控 Gemini 响应验证直接唤醒、单用户故障重试与启动恢复；不调用外部模型。

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeModel 通过闸门区分首段输出前后的等待，记录新输入和重启引起的取消与重试。
type fakeModel struct {
	databasePool *pgxpool.Pool
	mutex        sync.Mutex
	gates        map[string]chan struct{}
	stream       map[string]bool
	starts       map[string]int
	cancels      map[string]int
}

// serve 从真实请求的任务快照与工具回执决定下一步，驱动完整的登记、选择、回复与完成链路。
func (f *fakeModel) serve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Contents []struct {
			Parts []struct {
				Text     string `json:"text"`
				Response *struct {
					Name     string `json:"name"`
					Response struct {
						Output string `json:"output"`
					} `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	raw, err := io.ReadAll(r.Body)
	must(err)
	must(json.Unmarshal(raw, &body))
	var state struct {
		Active *struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"active_task"`
		Input *struct {
			ID      string `json:"id"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"next_unregistered_input"`
		Select bool `json:"needs_task_selection"`
	}
	lastTool, lastResult := "", ""
	for _, content := range body.Contents {
		for _, part := range content.Parts {
			if index := strings.Index(part.Text, `{"active_task":`); index >= 0 {
				must(json.Unmarshal([]byte(part.Text[index:]), &state))
			}
			if part.Response != nil {
				lastTool, lastResult = part.Response.Name, part.Response.Response.Output
			}
		}
	}
	part := map[string]any{"text": ""}
	label := ""
	var gate chan struct{}
	streaming := false
	if state.Input != nil {
		changes := []any{}
		if !strings.HasPrefix(state.Input.Message.Content, "supplement") {
			changes = append(changes, map[string]string{"title": state.Input.Message.Content, "description": state.Input.Message.Content})
		}
		part = call("record_task_input", map[string]any{"source_message_id": state.Input.ID, "changes": changes})
	} else if state.Active != nil {
		label = state.Active.Title
		answer := "RUNTIME-OK:" + label
		if strings.Contains(string(raw), answer) {
			part = call("update_task", map[string]string{"task_id": state.Active.ID, "status": "completed"})
			label = ""
		} else {
			f.mutex.Lock()
			gate, streaming = f.gates[label], f.stream[label]
			f.starts[label]++
			f.mutex.Unlock()
			part = map[string]any{"text": answer}
		}
	} else if state.Select {
		part = call("list_tasks", map[string]any{})
		if lastTool == "list_tasks" {
			var tasks []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			must(json.Unmarshal([]byte(lastResult), &tasks))
			for _, task := range tasks {
				if task.Status == "queued" {
					part = call("update_task", map[string]string{"task_id": task.ID, "status": "in_progress"})
					break
				}
			}
		}
	}
	// 已输出场景先送出一帧并保持响应未结束，让输入在真实助手占位之后到达。
	response := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}, "usageMetadata": map[string]int{"promptTokenCount": 10, "candidatesTokenCount": 1, "totalTokenCount": 11}}
	encoded, err := json.Marshal(response)
	must(err)
	w.Header().Set("Content-Type", "text/event-stream")
	if streaming {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		w.(http.Flusher).Flush()
	}
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			f.mutex.Lock()
			f.cancels[label]++
			f.mutex.Unlock()
			return
		}
	}
	if !streaming {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
	}
}

// call 生成 Gemini 原生工具调用，使后端执行真实工具与事务。
func call(name string, args any) map[string]any {
	return map[string]any{"functionCall": map[string]any{"id": uuid.NewString(), "name": name, "args": args}}
}

// hold 登记正文输出的阻塞点，afterOutput 决定是否先触发后端占位。
func (f *fakeModel) hold(label string, afterOutput bool) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.gates[label], f.stream[label] = make(chan struct{}), afterOutput
}

// release 放行同名任务当前与后续请求。
func (f *fakeModel) release(label string) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	close(f.gates[label])
}

// count 并发安全地读取模型启动或取消次数。
func (f *fakeModel) count(label string, cancelled bool) int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if cancelled {
		return f.cancels[label]
	}
	return f.starts[label]
}

// backend 记录一个真实服务进程，所有重启都先等待旧进程退出。
type backend struct {
	cmd  *exec.Cmd
	exit chan error
	dead bool
}

// client 在真实 WebSocket 上持续读回事件，并按消息 ID 去重记录可见回复。
type client struct {
	connection *websocket.Conn
	userID     uuid.UUID
	clientID   uuid.UUID
	done       chan struct{}
	mutex      sync.Mutex
	answers    map[string]string
	delivered  int
}

// send 允许指定重复消息 ID，验证重发去重与输入序号。
func (c *client) send(ctx context.Context, label string, id uuid.UUID) {
	must(wsjson.Write(ctx, c.connection, map[string]any{"type": "send_message", "client_message_id": id, "text": label}))
}

// must 在首次失败时终止命令，main 已登记的资源清理仍会执行。
func must(err error) {
	if err != nil {
		panic(err)
	}
}

// wait 在总上下文内等待可观察事实，失败时指出没有达到的条件。
func wait(ctx context.Context, label string, timeout time.Duration, condition func() bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		select {
		case <-ctx.Done():
			panic(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	panic("timeout: " + label)
}

// main 只接受专用空库；构建带竞态检测的后端，依次验证调度与恢复，退出清理全部验证数据。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	databaseURL := os.Getenv("RUNTIME_DATABASE_URL")
	parsed, err := url.Parse(databaseURL)
	must(err)
	if !strings.HasSuffix(parsed.Path, "_runtime") {
		panic("RUNTIME_DATABASE_URL must point to an empty dedicated database ending in _runtime")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	must(err)
	defer pool.Close()
	var count int
	must(pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM users)+(SELECT count(*) FROM service_settings)").Scan(&count))
	if count != 0 {
		panic("verification database must contain no users or service credentials")
	}
	var routingRemoved bool
	must(pool.QueryRow(ctx, "SELECT to_regclass('agent_instances') IS NULL AND to_regclass('agent_routes') IS NULL AND NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='main_input_insert')").Scan(&routingRemoved))
	if !routingRemoved {
		panic("apply the single-instance migration before verification")
	}
	directory, err := os.MkdirTemp("", "aether-runtime-verify-")
	must(err)
	fmt.Println("Local process logs:", directory)
	executable := filepath.Join(directory, "server")
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", executable, "./cmd/server")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	must(build.Run())
	fake := &fakeModel{databasePool: pool, gates: map[string]chan struct{}{}, stream: map[string]bool{}, starts: map[string]int{}, cancels: map[string]int{}}
	model := httptest.NewServer(http.HandlerFunc(fake.serve))
	defer model.Close()
	key := make([]byte, 32)
	_, err = rand.Read(key)
	must(err)
	store, err := services.New(pool, key, "dev")
	must(err)
	dummyKey, endpoint := "local-runtime-only", model.URL
	must(store.Update(ctx, services.IkunGemini, &dummyKey, &endpoint, nil))
	var processes []*backend
	var clients []*client
	defer func() {
		for _, c := range clients {
			_ = c.connection.CloseNow()
			<-c.done
		}
		for _, b := range processes {
			if !b.dead {
				_ = b.cmd.Process.Kill()
				<-b.exit
			}
		}
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, "DROP TRIGGER IF EXISTS verify_runtime_delete_fault ON messages; DROP TRIGGER IF EXISTS verify_runtime_step_fault ON steps; DROP FUNCTION IF EXISTS verify_runtime_fault(); DROP TABLE IF EXISTS verify_runtime_faults; DROP SEQUENCE IF EXISTS verify_runtime_fault_attempts")
		_, _ = pool.Exec(cleanup, "DELETE FROM users WHERE email LIKE '%@runtime.invalid'; DELETE FROM service_settings WHERE service='ikun_gemini'")
	}()
	// 同一端口先停后启，保持任意时刻只有一个执行进程；日志按重启次数分文件保存。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	port := fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
	must(listener.Close())
	start := func() *backend {
		logFile, err := os.Create(filepath.Join(directory, fmt.Sprintf("server-%d.log", len(processes))))
		must(err)
		cmd := exec.Command(executable)
		serverURL := *parsed
		parameters := serverURL.Query()
		parameters.Set("application_name", "aether-runtime-verification")
		serverURL.RawQuery = parameters.Encode()
		cmd.Env = append(os.Environ(), "APP_ENV=dev", "PORT="+port, "DATABASE_URL="+serverURL.String(), "DATABASE_MAX_CONNS=8", "AGENT_CAPACITY=2", "SERVICE_ENCRYPTION_KEY="+base64.StdEncoding.EncodeToString(key), "MAIN_MODEL=ikun:gemini-runtime-local", "FRONTEND_DIR=")
		cmd.Stdout, cmd.Stderr = logFile, logFile
		must(cmd.Start())
		b := &backend{cmd: cmd, exit: make(chan error, 1)}
		processes = append(processes, b)
		go func() { result := cmd.Wait(); _ = logFile.Close(); b.exit <- result }()
		wait(ctx, "backend health", 10*time.Second, func() bool {
			response, err := http.Get("http://127.0.0.1:" + port + "/healthz")
			if err != nil {
				return false
			}
			_ = response.Body.Close()
			return response.StatusCode == http.StatusOK
		})
		return b
	}
	current := start()
	// 采样真实模型日志，检查单用户串行和总并发名额；监视协程退出后才关闭数据库池。
	var invalidConcurrency atomic.Bool
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	defer func() { stopMonitor(); <-monitorDone }()
	go func() {
		defer close(monitorDone)
		for monitorCtx.Err() == nil {
			var bad bool
			err := pool.QueryRow(monitorCtx, "SELECT (SELECT count(*)>2 FROM model_calls WHERE outcome='in_flight') OR EXISTS(SELECT 1 FROM model_calls WHERE outcome='in_flight' GROUP BY user_id HAVING count(*)>1)").Scan(&bad)
			if err == nil && bad {
				invalidConcurrency.Store(true)
			}
			select {
			case <-monitorCtx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	newClient := func(label string, hold bool) *client {
		if hold {
			fake.hold(label, false)
		}
		q := database.New(pool)
		userID, err := q.UpsertUserByEmail(ctx, uuid.NewString()+"@runtime.invalid")
		must(err)
		token := uuid.NewString()
		hash := sha256.Sum256([]byte(token))
		must(q.CreateSession(ctx, database.CreateSessionParams{UserID: userID, TokenHash: hash[:]}))
		connection, _, err := websocket.Dial(ctx, "ws://127.0.0.1:"+port+"/ws", nil)
		must(err)
		must(wsjson.Write(ctx, connection, map[string]any{"type": "hello", "token": token}))
		var ready map[string]any
		must(wsjson.Read(ctx, connection, &ready))
		if ready["authenticated"] != true {
			panic("WebSocket authentication failed")
		}
		c := &client{connection: connection, userID: userID, clientID: uuid.New(), done: make(chan struct{}), answers: map[string]string{}}
		clients = append(clients, c)
		go func() {
			defer close(c.done)
			for {
				var event struct {
					Type     string `json:"type"`
					Messages []struct {
						ID     string `json:"id"`
						Sender string `json:"sender"`
						Text   string `json:"text"`
					} `json:"messages"`
				}
				if wsjson.Read(ctx, connection, &event) != nil {
					return
				}
				c.mutex.Lock()
				if event.Type == "delivered" {
					c.delivered++
				}
				for _, message := range event.Messages {
					if message.Sender == "main" {
						c.answers[message.ID] = message.Text
					}
				}
				c.mutex.Unlock()
			}
		}()
		if label != "" {
			c.send(ctx, label, c.clientID)
		}
		return c
	}
	// 完成以数据库正文、任务状态和持久待办共同判断，浏览器推送另外检查。
	complete := func(c *client, label string) {
		wait(ctx, "one committed reply and completed task: "+label, 12*time.Second, func() bool {
			var replies int
			var done bool
			must(pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE user_id=$1 AND visible AND role='assistant' AND content->>'content'=$2", c.userID, "RUNTIME-OK:"+label).Scan(&replies))
			must(pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM tasks WHERE user_id=$1 AND title=$2 AND status='completed') AND NOT EXISTS(SELECT 1 FROM main_work WHERE user_id=$1)", c.userID, label).Scan(&done))
			if replies > 1 {
				panic("duplicate reply: " + label)
			}
			return replies == 1 && done
		})
	}
	first := newClient("first", true)
	wait(ctx, "direct input wake", 2*time.Second, func() bool { return fake.count("first", false) == 1 })
	second := newClient("second", true)
	wait(ctx, "second concurrent user", 2*time.Second, func() bool { return fake.count("second", false) == 1 })
	queued := newClient("queued", true)
	wait(ctx, "full-capacity input delivered", 2*time.Second, func() bool { queued.mutex.Lock(); defer queued.mutex.Unlock(); return queued.delivered == 1 })
	if fake.count("queued", false) != 0 {
		panic("third user bypassed capacity")
	}
	// 两个模型都在等待，此时数据库不应保留任何会话级用户执行锁。
	wait(ctx, "no advisory execution locks while models wait", 2*time.Second, func() bool {
		must(pool.QueryRow(ctx, "SELECT count(*) FROM pg_locks AS l JOIN pg_stat_activity AS a ON a.pid=l.pid WHERE a.application_name=$1 AND l.locktype='advisory'", "aether-runtime-verification").Scan(&count))
		return count == 0
	})
	fake.release("first")
	complete(first, "first")
	wait(ctx, "queued user starts after release", 2*time.Second, func() bool { return fake.count("queued", false) == 1 })
	fmt.Println("PASS direct wake, concurrent users and capacity queue")
	second.send(ctx, "supplement-before-output", uuid.New())
	wait(ctx, "new input cancels thinking", 3*time.Second, func() bool { return fake.count("second", true) == 1 && fake.count("second", false) == 2 })
	queued.send(ctx, "queued", queued.clientID)
	wait(ctx, "duplicate delivered", 2*time.Second, func() bool { queued.mutex.Lock(); defer queued.mutex.Unlock(); return queued.delivered == 2 })
	must(pool.QueryRow(ctx, "SELECT count(*) FROM messages WHERE user_id=$1 AND client_message_id=$2", queued.userID, queued.clientID).Scan(&count))
	if count != 1 {
		panic("duplicate input persisted")
	}
	fake.release("second")
	fake.release("queued")
	complete(second, "second")
	complete(queued, "queued")
	wait(ctx, "WebSocket reply push", 3*time.Second, func() bool { queued.mutex.Lock(); defer queued.mutex.Unlock(); return len(queued.answers) == 1 })
	fmt.Println("PASS input cancellation, duplicate resend and WebSocket delivery")
	// 首段正文已经占位时，补充输入必须等这批输出结束，不取消正在生成的回复。
	fake.hold("streamed", true)
	streamed := newClient("streamed", false)
	wait(ctx, "assistant placeholder", 3*time.Second, func() bool {
		var generating bool
		must(pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM messages WHERE user_id=$1 AND generating)", streamed.userID).Scan(&generating))
		return generating
	})
	streamed.send(ctx, "supplement-after-output", uuid.New())
	wait(ctx, "post-output input delivered", 2*time.Second, func() bool { streamed.mutex.Lock(); defer streamed.mutex.Unlock(); return streamed.delivered == 2 })
	if fake.count("streamed", true) != 0 {
		panic("new input cancelled an already reserved reply")
	}
	fake.release("streamed")
	complete(streamed, "streamed")
	disconnected := newClient("disconnected", true)
	wait(ctx, "disconnected work started", 3*time.Second, func() bool { return fake.count("disconnected", false) == 1 })
	must(disconnected.connection.CloseNow())
	<-disconnected.done
	fake.release("disconnected")
	complete(disconnected, "disconnected")
	fmt.Println("PASS output ordering and work survives browser disconnect")
	// 直接写库不再触发运行；只有对应用户的新唤醒或进程启动恢复能调度它。
	persisted := newClient("before-recovery", false)
	complete(persisted, "before-recovery")
	insert := func(c *client, label string) uuid.UUID {
		id := uuid.New()
		must(pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			q := database.New(tx)
			if err := q.LockUserMessages(ctx, c.userID); err != nil {
				return err
			}
			content, err := json.Marshal(map[string]string{"role": "user", "content": label})
			if err != nil {
				return err
			}
			_, err = q.InsertUserMessage(ctx, database.InsertUserMessageParams{UserID: c.userID, ClientMessageID: &id, Content: content})
			return err
		}))
		return id
	}
	// 只在本专用验证库安装故障触发器；序列不随事务回滚，用它观察真实失败与退避重试次数。
	_, err = pool.Exec(ctx, `
CREATE TABLE verify_runtime_faults(user_id uuid PRIMARY KEY, phase text NOT NULL, error_code text NOT NULL);
CREATE SEQUENCE verify_runtime_fault_attempts;
CREATE FUNCTION verify_runtime_fault() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_user uuid; failure_code text;
BEGIN
    IF TG_OP='DELETE' THEN target_user := OLD.user_id; ELSE target_user := NEW.user_id; END IF;
    SELECT error_code INTO failure_code FROM verify_runtime_faults WHERE user_id=target_user AND phase=TG_TABLE_NAME;
    IF FOUND THEN
        PERFORM nextval('verify_runtime_fault_attempts');
        RAISE EXCEPTION USING ERRCODE=failure_code, MESSAGE='local runtime verification fault';
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; ELSE RETURN NEW; END IF;
END;
$$;
CREATE TRIGGER verify_runtime_delete_fault BEFORE DELETE ON messages FOR EACH ROW EXECUTE FUNCTION verify_runtime_fault();
CREATE TRIGGER verify_runtime_step_fault BEFORE INSERT ON steps FOR EACH ROW EXECUTE FUNCTION verify_runtime_fault();`)
	must(err)
	attempts := func() int {
		var value int
		must(pool.QueryRow(ctx, "SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM verify_runtime_fault_attempts").Scan(&value))
		return value
	}
	untouchedID := insert(persisted, "no-global-scan")
	failed := newClient("", false)
	_, err = pool.Exec(ctx, "INSERT INTO messages(user_id,role,content,generating) VALUES($1,'assistant','{\"role\":\"assistant\"}',true)", failed.userID)
	must(err)
	_, err = pool.Exec(ctx, "INSERT INTO verify_runtime_faults VALUES($1,'messages','40001')", failed.userID)
	must(err)
	failed.send(ctx, "temporary-init", failed.clientID)
	wait(ctx, "initialization failure injected", 2*time.Second, func() bool { return attempts() >= 1 })
	healthy := newClient("healthy-during-retry", false)
	complete(healthy, "healthy-during-retry")
	observation := time.Now()
	wait(ctx, "no global scan while one user retries", 8*time.Second, func() bool {
		if fake.count("no-global-scan", false) != 0 {
			panic("local retry scanned and started another user's unwoken input")
		}
		return time.Since(observation) >= 6*time.Second
	})
	if attempts() < 3 || attempts() > 5 {
		panic("temporary failure did not use per-user backoff")
	}
	var markedFailed bool
	must(pool.QueryRow(ctx, "SELECT main_failed_through_sequence IS NOT NULL FROM users WHERE id=$1", failed.userID).Scan(&markedFailed))
	if markedFailed {
		panic("temporary database failure was marked as a business failure")
	}
	_, err = pool.Exec(ctx, "DELETE FROM verify_runtime_faults WHERE user_id=$1", failed.userID)
	must(err)
	complete(failed, "temporary-init")
	persisted.send(ctx, "no-global-scan", untouchedID)
	complete(persisted, "no-global-scan")
	fmt.Println("PASS initialization retries only the failed user, frees capacity, and never scans global work")
	// 同样覆盖运行循环里的临时故障；新的用户输入解除退避，永久错误则写失败位置并停止。
	loopFailure := newClient("", false)
	_, err = pool.Exec(ctx, "INSERT INTO verify_runtime_faults VALUES($1,'steps','55P03')", loopFailure.userID)
	must(err)
	before := attempts()
	loopFailure.send(ctx, "temporary-loop", loopFailure.clientID)
	wait(ctx, "loop failure retries", 4*time.Second, func() bool { return attempts() >= before+2 })
	_, err = pool.Exec(ctx, "DELETE FROM verify_runtime_faults WHERE user_id=$1", loopFailure.userID)
	must(err)
	loopFailure.send(ctx, "supplement-retry", uuid.New())
	complete(loopFailure, "temporary-loop")
	permanent := newClient("", false)
	_, err = pool.Exec(ctx, "INSERT INTO verify_runtime_faults VALUES($1,'steps','22023')", permanent.userID)
	must(err)
	before = attempts()
	permanent.send(ctx, "permanent-error", permanent.clientID)
	wait(ctx, "permanent database failure stops", 2*time.Second, func() bool {
		must(pool.QueryRow(ctx, "SELECT main_failed_through_sequence IS NOT NULL FROM users WHERE id=$1", permanent.userID).Scan(&markedFailed))
		return markedFailed
	})
	observation = time.Now()
	wait(ctx, "permanent error is not retried", 3*time.Second, func() bool {
		if attempts() != before+1 {
			panic("permanent database error was retried")
		}
		return time.Since(observation) >= 2*time.Second
	})
	fmt.Println("PASS runtime retry, fresh input during backoff and permanent failure exclusion")
	// 先杀掉执行进程并等待退出，再持久化一条离线输入；新进程同时恢复进行中的任务和未唤醒输入。
	crashed := newClient("crash", true)
	wait(ctx, "crash work started", 3*time.Second, func() bool { return fake.count("crash", false) == 1 })
	must(current.cmd.Process.Kill())
	<-current.exit
	current.dead = true
	insert(persisted, "offline-input")
	// 启动读取暂时超时后会重新读取，成功恢复一次后彻底停止全局补查。
	lock, err := pool.Begin(ctx)
	must(err)
	defer lock.Rollback(ctx)
	_, err = lock.Exec(ctx, "LOCK TABLE users IN ACCESS EXCLUSIVE MODE")
	must(err)
	current = start()
	wait(ctx, "startup snapshot retries after a temporary timeout", 5*time.Second, func() bool {
		log, err := os.ReadFile(filepath.Join(directory, fmt.Sprintf("server-%d.log", len(processes)-1)))
		must(err)
		return strings.Contains(string(log), "retrying startup")
	})
	must(lock.Commit(ctx))
	wait(ctx, "startup resumes interrupted task", 3*time.Second, func() bool { return fake.count("crash", false) == 2 })
	fake.release("crash")
	complete(crashed, "crash")
	complete(persisted, "offline-input")
	fmt.Println("PASS startup recovery of interrupted work and offline input")
	// 快速用户交替与连续独立目标覆盖 Runner 退出与新输入交错，所有输入最终都必须登记并完成。
	var bursts []*client
	for i := 0; i < 12; i++ {
		bursts = append(bursts, newClient(fmt.Sprintf("burst-%d", i), false))
	}
	for i, c := range bursts {
		complete(c, fmt.Sprintf("burst-%d", i))
	}
	for i := 0; i < 12; i++ {
		label := fmt.Sprintf("handoff-%d", i)
		persisted = newClient(label, false)
		complete(persisted, label)
		next := label + "-next"
		persisted.send(ctx, next, uuid.New())
		complete(persisted, next)
	}
	if invalidConcurrency.Load() {
		panic("sampled per-user or global concurrency invariant violated")
	}
	fmt.Println("PASS burst inputs, per-user handoff and sampled concurrency invariants")
	// 正常退出给当前任务排空时间；HTTP 停止后模型仍能完成并保存结果。
	drain := newClient("drain", true)
	wait(ctx, "drain work started", 3*time.Second, func() bool { return fake.count("drain", false) == 1 })
	must(current.cmd.Process.Signal(syscall.SIGTERM))
	wait(ctx, "HTTP stopped before runtime finishes", 2*time.Second, func() bool {
		response, err := http.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil {
			return true
		}
		_ = response.Body.Close()
		return false
	})
	fake.release("drain")
	complete(drain, "drain")
	select {
	case result := <-current.exit:
		current.dead = true
		must(result)
	case <-time.After(8 * time.Second):
		panic("backend did not finish graceful drain")
	}
	for i := range processes {
		log, err := os.ReadFile(filepath.Join(directory, fmt.Sprintf("server-%d.log", i)))
		must(err)
		if strings.Contains(string(log), "DATA RACE") {
			panic("race detector reported a data race")
		}
	}
	fmt.Println("PASS graceful drain and race detector")
	fmt.Println("All local single-instance runtime scenarios passed; no external model or production service was used.")
}
