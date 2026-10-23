package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type runtimeFixture struct {
	t          *testing.T
	pool       *pgxpool.Pool
	poolConfig *pgxpool.Config
	userID     uuid.UUID
}

func newRuntimeFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for isolated PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "agent_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close(cleanup)
	})
	settings, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	settings.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	settings.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	files, err := filepath.Glob("../../db/migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		up := strings.Split(string(body), "-- +goose Down")[0]
		if _, err := pool.Exec(ctx, up); err != nil {
			t.Fatalf("apply %s in isolated schema: %v", file, err)
		}
	}
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, "INSERT INTO users(email) VALUES ('runtime@example.test') RETURNING id").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	return &runtimeFixture{t: t, pool: pool, poolConfig: settings, userID: userID}
}

func (f *runtimeFixture) secondPool() *pgxpool.Pool {
	f.t.Helper()
	pool, err := pgxpool.NewWithConfig(context.Background(), f.poolConfig.Copy())
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(pool.Close)
	return pool
}

func (f *runtimeFixture) userMessage(text string) {
	f.t.Helper()
	id := uuid.New()
	_, err := database.New(f.pool).InsertUserMessage(context.Background(), database.InsertUserMessageParams{UserID: f.userID, Text: text, ClientMessageID: &id})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *runtimeFixture) subagent(task string) uuid.UUID {
	f.t.Helper()
	queries := database.New(f.pool)
	id, err := queries.CreateSubagent(context.Background(), f.userID)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := deliverText(context.Background(), queries, agentAddress{userID: f.userID, subagentID: &id}, "main", nil, task); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *runtimeFixture) count(query string, args ...any) int {
	f.t.Helper()
	var count int
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

type runtimeModelRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []toolCall      `json:"tool_calls"`
	} `json:"messages"`
}

func (r runtimeModelRequest) lastText() string {
	var content string
	json.Unmarshal(r.Messages[len(r.Messages)-1].Content, &content)
	return content
}

func (r runtimeModelRequest) lastRole() string { return r.Messages[len(r.Messages)-1].Role }

type runtimeTransport func(*http.Request) (*http.Response, error)

func (t runtimeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

func newRuntimeRunner(pool *pgxpool.Pool, server *httptest.Server) *Runner {
	runner := NewRunner(pool, config.Config{MainModel: "main-test", SubagentModel: "sub-test", OpenRouterAPIKey: "test-key", SERPAPIKey: "test-key", ParallelAPIKey: "test-key"})
	client := &http.Client{Timeout: 5 * time.Second, Transport: runtimeTransport(func(request *http.Request) (*http.Response, error) {
		copy := request.Clone(request.Context())
		copy.URL.Scheme = "http"
		copy.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(copy)
	})}
	runner.httpClient = client
	runner.webTools.httpClient = client
	return runner
}

func startRuntime(t *testing.T, runner *Runner) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go runner.Start(ctx)
	t.Cleanup(func() {
		cancel()
		waitForRuntime(t, "runtime stops", func() bool { runner.mutex.Lock(); defer runner.mutex.Unlock(); return len(runner.active) == 0 })
	})
	return cancel
}

func waitForRuntime(t *testing.T, description string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func runtimeTool(name string, args any) toolCall {
	body, _ := json.Marshal(args)
	var call toolCall
	call.ID = "call_" + uuid.NewString()
	call.Function.Name = name
	call.Function.Arguments = string(body)
	return call
}

func writeRuntimeReply(w http.ResponseWriter, calls ...toolCall) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "", "tool_calls": calls}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "cost": 0}})
}

func decodeRuntimeModel(t *testing.T, w http.ResponseWriter, request *http.Request) runtimeModelRequest {
	t.Helper()
	var result runtimeModelRequest
	if err := json.NewDecoder(request.Body).Decode(&result); err != nil {
		t.Errorf("decode model request: %v", err)
		w.WriteHeader(400)
	}
	return result
}

func TestRuntimeMainSubagentResearchFlow(t *testing.T) {
	f := newRuntimeFixture(t)
	var searches, fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/search":
			searches.Add(1)
			fmt.Fprint(w, `{"organic":[{"title":"Reference","link":"https://example.com/fact","snippet":"verified fact"}]}`)
			return
		case "/v1/extract":
			fetches.Add(1)
			fmt.Fprint(w, `{"extract_id":"test","results":[{"full_content":"Verified source text","excerpts":[]}],"errors":[]}`)
			return
		}
		model := decodeRuntimeModel(t, w, request)
		if model.Model == "main-test" {
			switch {
			case model.lastText() == "research":
				writeRuntimeReply(w, runtimeTool("assign_subagents", map[string]any{"tasks": []string{"Find and read reference"}}))
			case strings.HasPrefix(model.lastText(), "Message from subagent "):
				writeRuntimeReply(w, runtimeTool("send_message", map[string]string{"to": "user", "text": "Verified: https://example.com/fact"}))
			default:
				writeRuntimeReply(w)
			}
		} else {
			switch {
			case model.lastRole() == "user":
				writeRuntimeReply(w, runtimeTool("search", map[string]string{"query": "reference"}), runtimeTool("fetch_url", map[string]string{"url": "https://example.com/fact"}))
			case model.lastText() == "delivered":
				writeRuntimeReply(w)
			default:
				if !strings.Contains(model.lastText(), "Verified source text") {
					t.Error("subagent did not receive extracted source")
				}
				writeRuntimeReply(w, runtimeTool("send_message", map[string]string{"to": "main", "text": "Verified: https://example.com/fact"}))
			}
		}
	}))
	t.Cleanup(server.Close)
	runner := newRuntimeRunner(f.pool, server)
	startRuntime(t, runner)
	f.userMessage("research")
	runner.Wake(f.userID)
	waitForRuntime(t, "Main receives research and replies to user", func() bool {
		return f.count("SELECT count(*) FROM messages WHERE subagent_id IS NULL AND role='assistant' AND content::text LIKE '%Verified:%'") == 1
	})
	waitForRuntime(t, "all model requests finish", func() bool { return f.count("SELECT count(*) FROM steps WHERE finished_at IS NULL") == 0 })
	if searches.Load() != 1 || fetches.Load() != 1 {
		t.Fatalf("upstream calls search=%d fetch=%d", searches.Load(), fetches.Load())
	}
	rows, err := database.New(f.pool).ListChatRowsAfter(context.Background(), database.ListChatRowsAfterParams{UserID: f.userID, ID: uuid.Nil})
	if err != nil {
		t.Fatal(err)
	}
	var visible []MessageToUser
	for _, row := range rows {
		if row.Role == "assistant" {
			visible = append(visible, MessagesToUser(row.Content)...)
		}
	}
	if len(visible) != 1 || visible[0].Text != "Verified: https://example.com/fact" {
		t.Fatalf("visible Main messages %#v", visible)
	}
	if f.count("SELECT count(*) FROM messages WHERE sender='subagent'") != 1 {
		t.Fatal("missing subagent-to-Main delivery")
	}
	if f.count("SELECT count(*) FROM steps WHERE subagent_id IS NOT NULL AND finished_at IS NOT NULL") == 0 {
		t.Fatal("subagent usage was not recorded")
	}
}

func TestRuntimeThreeSlotsAcrossInstancesAndPersistentQueue(t *testing.T) {
	f := newRuntimeFixture(t)
	var active, maximum, entered atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		decodeRuntimeModel(t, w, request)
		current := active.Add(1)
		entered.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		defer active.Add(-1)
		select {
		case <-request.Context().Done():
			return
		case <-release:
		}
		writeRuntimeReply(w)
	}))
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); server.Close() })
	for i := 0; i < 4; i++ {
		f.subagent(fmt.Sprintf("task %d", i))
	}
	first := newRuntimeRunner(f.pool, server)
	second := newRuntimeRunner(f.secondPool(), server)
	startRuntime(t, first)
	startRuntime(t, second)
	waitForRuntime(t, "three concurrent subagents", func() bool { return entered.Load() == 3 })
	time.Sleep(1200 * time.Millisecond)
	if entered.Load() != 3 || maximum.Load() != 3 {
		t.Fatalf("executed beyond three user slots: entered=%d max=%d", entered.Load(), maximum.Load())
	}
	if f.count("SELECT count(*) FROM subagents WHERE status='running'") != 3 || f.count("SELECT count(*) FROM messages WHERE subagent_id IS NOT NULL AND sequence IS NULL") != 1 {
		t.Fatal("fourth task was not retained in persistent queue")
	}
	releaseOnce.Do(func() { close(release) })
	waitForRuntime(t, "queued fourth task completes", func() bool {
		return entered.Load() == 4 && f.count("SELECT count(*) FROM subagents WHERE status='idle'") == 4
	})
	if maximum.Load() > 3 {
		t.Fatalf("cross-instance concurrency was %d", maximum.Load())
	}
}

func TestRuntimeRecoversReadInputAndToolContinuation(t *testing.T) {
	f := newRuntimeFixture(t)
	f.userMessage("recover")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		decodeRuntimeModel(t, w, request)
		switch calls.Add(1) {
		case 1:
			writeRuntimeReply(w, runtimeTool("send_message", map[string]string{"to": "user", "text": "survives restart"}))
		case 2:
			<-request.Context().Done()
		default:
			writeRuntimeReply(w)
		}
	}))
	t.Cleanup(server.Close)
	first := newRuntimeRunner(f.pool, server)
	connection, err := f.pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stepID, err := first.prepareStep(context.Background(), connection, agentAddress{userID: f.userID})
	connection.Release()
	if err != nil {
		t.Fatal(err)
	}
	if stepID == uuid.Nil || f.count("SELECT count(*) FROM messages WHERE sequence IS NULL") != 0 {
		t.Fatal("fixture did not persist read input with unfinished model request")
	}
	cancel := startRuntime(t, first)
	waitForRuntime(t, "next model request after tool output", func() bool { return calls.Load() == 2 })
	if f.count("SELECT count(*) FROM steps WHERE finished_at IS NULL") != 1 {
		t.Fatal("tool output did not leave a durable continuation")
	}
	cancel()
	waitForRuntime(t, "first runtime releases its lock", func() bool { first.mutex.Lock(); defer first.mutex.Unlock(); return len(first.active) == 0 })
	second := newRuntimeRunner(f.secondPool(), server)
	startRuntime(t, second)
	waitForRuntime(t, "unfinished continuation recovers", func() bool {
		return calls.Load() == 3 && f.count("SELECT count(*) FROM steps WHERE finished_at IS NULL") == 0
	})
	if f.count("SELECT count(*) FROM messages WHERE role='assistant' AND content::text LIKE '%survives restart%'") != 1 {
		t.Fatal("restart duplicated or lost the visible message")
	}
}

func TestRuntimeFailureTimeoutAndReawakening(t *testing.T) {
	f := newRuntimeFixture(t)
	expired := f.subagent("expired task")
	failure := f.subagent("bad model request")
	runningTimeout := f.subagent("running timeout")
	if _, err := f.pool.Exec(context.Background(), "UPDATE subagents SET status='running',started_at=now()-interval '16 minutes' WHERE id=$1", expired); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), "UPDATE messages SET sequence=nextval('message_sequence') WHERE subagent_id=$1", expired); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), "UPDATE subagents SET status='running',started_at=now()-interval '14 minutes 59.5 seconds' WHERE id=$1", runningTimeout); err != nil {
		t.Fatal(err)
	}
	var subCalls atomic.Int32
	var allowSuccess atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		model := decodeRuntimeModel(t, w, request)
		if model.Model == "sub-test" {
			subCalls.Add(1)
			if model.lastText() == "running timeout" {
				<-request.Context().Done()
				return
			}
			if !allowSuccess.Load() {
				http.Error(w, "invalid model request", http.StatusBadRequest)
				return
			}
		}
		writeRuntimeReply(w)
	}))
	t.Cleanup(server.Close)
	runner := newRuntimeRunner(f.pool, server)
	startRuntime(t, runner)
	waitForRuntime(t, "all failures reported to Main", func() bool {
		return f.count("SELECT count(*) FROM subagents WHERE status='failed'") == 3 && f.count("SELECT count(*) FROM messages WHERE sender='system'") == 3
	})
	if subCalls.Load() != 2 {
		t.Fatalf("expired activation called model or permanent failure retried: %d", subCalls.Load())
	}
	if f.count("SELECT count(*) FROM steps WHERE subagent_id IS NOT NULL AND finished_at IS NULL") != 0 {
		t.Fatal("failure left unfinished steps")
	}
	connection, err := f.pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	queries := database.New(connection)
	for slot := 0; slot < 3; slot++ {
		key := fmt.Sprintf("subagent-slot:%s:%d", f.userID, slot)
		locked, err := queries.TryLockRuntime(context.Background(), key)
		if err != nil || !locked {
			connection.Release()
			t.Fatalf("failed activation retained slot %d: %v", slot, err)
		}
		queries.UnlockRuntime(context.Background(), key)
	}
	connection.Release()
	allowSuccess.Store(true)
	if err := deliverText(context.Background(), database.New(f.pool), agentAddress{userID: f.userID, subagentID: &failure}, "main", nil, "try again"); err != nil {
		t.Fatal(err)
	}
	runner.Wake(f.userID)
	waitForRuntime(t, "failed agent wakes on new Main message", func() bool {
		return f.count("SELECT count(*) FROM subagents WHERE id=$1 AND status='idle' AND started_at>now()-interval '1 minute'", failure) == 1
	})
}
