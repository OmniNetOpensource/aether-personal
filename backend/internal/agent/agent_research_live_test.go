package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/shopspring/decimal"
)

type researchCase struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	Prompts           []string `json:"prompts"`
	RequireDelegation bool     `json:"require_delegation,omitempty"`
}

type researchHTTPTrace struct {
	StartedAt      time.Time       `json:"started_at"`
	Seconds        float64         `json:"seconds"`
	Method         string          `json:"method"`
	URL            string          `json:"url"`
	Actor          string          `json:"actor,omitempty"`
	Status         int             `json:"status"`
	Error          string          `json:"error,omitempty"`
	PromptVerified bool            `json:"prompt_verified,omitempty"`
	MainPromptHash string          `json:"main_prompt_sha256,omitempty"`
	Request        json.RawMessage `json:"request,omitempty"`
	Response       json.RawMessage `json:"response,omitempty"`
	ResponseText   string          `json:"response_text,omitempty"`
}

type researchObserver struct {
	mutex  sync.Mutex
	traces []researchHTTPTrace
}

func (o *researchObserver) RoundTrip(request *http.Request) (*http.Response, error) {
	started := time.Now()
	trace := researchHTTPTrace{StartedAt: started.UTC(), Method: request.Method, URL: request.URL.String(), Error: "request still in flight at snapshot"}
	o.mutex.Lock()
	index := len(o.traces)
	o.traces = append(o.traces, trace)
	o.mutex.Unlock()
	trace.Error = ""
	response, err := o.roundTrip(request, &trace)
	trace.Seconds = time.Since(started).Seconds()
	if err != nil {
		trace.Error = err.Error()
	}
	o.mutex.Lock()
	o.traces[index] = trace
	o.mutex.Unlock()
	return response, err
}

func (o *researchObserver) roundTrip(request *http.Request, trace *researchHTTPTrace) (*http.Response, error) {
	copy := request.Clone(request.Context())
	if request.Body != nil {
		body, err := io.ReadAll(io.LimitReader(request.Body, maxWebResponseBytes+1))
		if err != nil {
			return nil, err
		}
		request.Body.Close()
		if len(body) > maxWebResponseBytes {
			return nil, fmt.Errorf("research replay request exceeded capture bound")
		}
		if request.URL.Host == "openrouter.ai" && request.URL.Path == "/api/v1/chat/completions" {
			var input map[string]json.RawMessage
			if err := json.Unmarshal(body, &input); err != nil {
				return nil, fmt.Errorf("research replay model request: %w", err)
			}
			var tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if err := json.Unmarshal(input["tools"], &tools); err != nil {
				return nil, err
			}
			trace.Actor = "subagent"
			for _, tool := range tools {
				if tool.Function.Name == "assign_subagents" {
					trace.Actor = "main"
				}
			}
			var messages []map[string]json.RawMessage
			if err := json.Unmarshal(input["messages"], &messages); err != nil || len(messages) == 0 {
				return nil, fmt.Errorf("research replay model request has no readable messages")
			}
			var role, prompt string
			if json.Unmarshal(messages[0]["role"], &role) != nil || role != "system" || json.Unmarshal(messages[0]["content"], &prompt) != nil {
				return nil, fmt.Errorf("research replay model request has no first system prompt")
			}
			if trace.Actor == "main" {
				if prompt != mainPrompt {
					return nil, fmt.Errorf("research replay refused changed production Main prompt")
				}
				trace.MainPromptHash = researchHash([]byte(prompt))
			} else if prompt != subagentPrompt {
				return nil, fmt.Errorf("research replay refused changed Subagent prompt")
			}
			trace.PromptVerified = true
			input["temperature"] = json.RawMessage("0")
			body, _ = json.Marshal(input)
		}
		if json.Valid(body) {
			trace.Request = append(json.RawMessage(nil), body...)
		}
		copy.Body = io.NopCloser(bytes.NewReader(body))
		copy.ContentLength = int64(len(body))
	}
	response, err := http.DefaultTransport.RoundTrip(copy)
	if err != nil {
		return nil, err
	}
	trace.Status = response.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxWebResponseBytes+1))
	response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	if json.Valid(body) {
		trace.Response = append(json.RawMessage(nil), body...)
	} else {
		trace.ResponseText = string(body)
	}
	return response, nil
}

type researchVisible struct {
	MessageID  string    `json:"message_id"`
	ToolCallID string    `json:"tool_call_id"`
	Text       string    `json:"text"`
	CreatedAt  time.Time `json:"created_at"`
}

type researchTurn struct {
	Prompt       string            `json:"prompt"`
	Status       string            `json:"status"`
	Error        string            `json:"error,omitempty"`
	FirstVisible *float64          `json:"first_visible_seconds"`
	LastVisible  *float64          `json:"last_visible_seconds"`
	Quiescence   *float64          `json:"quiescence_seconds"`
	Messages     []researchVisible `json:"messages"`
}

type researchMetrics struct {
	SubagentReports    int                       `json:"subagent_reports"`
	SubagentCount      int                       `json:"subagent_count"`
	ModelCalls         map[string]int            `json:"model_calls"`
	ToolCalls          map[string]map[string]int `json:"tool_calls"`
	SuccessfulWebCalls map[string]map[string]int `json:"successful_web_calls"`
	InputTokens        *int64                    `json:"input_tokens"`
	OutputTokens       *int64                    `json:"output_tokens"`
	ModelCost          *string                   `json:"model_cost_usd"`
	KnownModelCost     string                    `json:"known_model_cost_usd"`
	CostKnownCalls     int                       `json:"cost_known_calls"`
	TokenKnownCalls    int                       `json:"token_known_calls"`
	SupplierCost       *string                   `json:"supplier_cost_usd"`
	HTTPFailures       int                       `json:"http_failures"`
	PersistedErrors    int                       `json:"persisted_errors"`
	ToolErrors         int                       `json:"tool_errors"`
}

type researchCell struct {
	Case           researchCase        `json:"case"`
	Order          int                 `json:"order"`
	Status         string              `json:"status"`
	StartedAt      time.Time           `json:"started_at"`
	FinishedAt     time.Time           `json:"finished_at"`
	MainPromptHash string              `json:"main_prompt_sha256"`
	SubagentModel  string              `json:"subagent_model"`
	Model          string              `json:"model"`
	Turns          []researchTurn      `json:"turns"`
	Metrics        researchMetrics     `json:"metrics"`
	Messages       json.RawMessage     `json:"messages"`
	Steps          json.RawMessage     `json:"steps"`
	Subagents      json.RawMessage     `json:"subagents"`
	Traces         []researchHTTPTrace `json:"http_traces"`
	Errors         []string            `json:"errors,omitempty"`
}

// Live research replay uses production prompts and providers after explicit opt-in.
func TestLiveAgentResearch(t *testing.T) {
	if os.Getenv("RUN_LIVE_AGENT_RESEARCH") != "1" {
		t.Skip("set RUN_LIVE_AGENT_RESEARCH=1 to replay real-provider agent research")
	}
	output := os.Getenv("AGENT_RESEARCH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("AGENT_RESEARCH_OUTPUT must be an explicit absolute directory")
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(output); err != nil || len(entries) != 0 {
		t.Fatal("AGENT_RESEARCH_OUTPUT must be empty to preserve previous runs")
	}
	settings, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("TEST_DATABASE_URL") == "" || settings.SERPAPIKey == "" || settings.ParallelAPIKey == "" || settings.EveryInfraAPIKey == "" {
		t.Fatal("TEST_DATABASE_URL and all web provider keys are required")
	}
	data, err := os.ReadFile("testdata/agent_research.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []researchCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	redact := researchRedactor(settings)
	load, loadError := exec.Command("uptime").CombinedOutput()
	if loadError != nil {
		load = []byte("unavailable: " + loadError.Error())
	}
	manifest := map[string]any{
		"started_at": time.Now().UTC(), "cases": cases, "main_model": settings.MainModel,
		"subagent_model": settings.SubagentModel, "architecture": "Main and Subagents share web execution tools; Main may execute directly or delegate",
		"temperature": 0, "case_deadline_seconds": 300, "replications": 1,
		"environment": map[string]any{"goos": runtime.GOOS, "logical_cpus": runtime.NumCPU(), "uptime_and_load_at_start": strings.TrimSpace(string(load)), "case_order": "sequential"},
		"main_prompt": mainPrompt, "main_prompt_sha256": researchHash([]byte(mainPrompt)), "subagent_prompt": subagentPrompt,
		"main_tools": mainTools, "subagent_tools": subagentTools,
		"git_head": researchGit("rev-parse", "HEAD"), "git_dirty": researchGit("status", "--porcelain=v1"),
		"source_sha256": researchSourceHashes(t), "case_filter": os.Getenv("AGENT_RESEARCH_CASE"),
		"limitations": []string{"One-sample production regression replay, not a comparison or performance benchmark.", "Production Main and Subagent prompts and tool lists are preserved; temperature 0 is applied to both roles for this replay only.", "Model sampling and live search/provider responses may vary; temperature 0 does not guarantee determinism.", "Cost totals are model API cost only; supplier billing is unknown. Missing cost is null, not zero.", "Visible timing is Postgres message created_at minus local insertion start, assuming local clocks agree; not browser/WebSocket end-to-end latency. Quiescence is polled at 150 ms intervals.", "Case 06 waits for quiescence before follow-up; in-flight user interruption is not covered.", "Other local workloads, network and provider load vary.", "Quiescence plus a visible message is not a quality score or proof of a final answer; user reviews all visible messages."},
	}
	researchWriteJSON(t, output, "manifest.json", manifest, redact)
	var cells []researchCell
	for _, item := range cases {
		if filter := os.Getenv("AGENT_RESEARCH_CASE"); filter != "" && filter != item.ID {
			continue
		}
		cell := researchCell{Case: item, Order: len(cells) + 1, StartedAt: time.Now().UTC(), Status: "setup_failed", Model: settings.MainModel, SubagentModel: settings.SubagentModel, MainPromptHash: researchHash([]byte(mainPrompt))}
		t.Run(item.ID, func(t *testing.T) {
			defer func() {
				cell.FinishedAt = time.Now().UTC()
				researchWriteJSON(t, output, item.ID+".json", cell, redact)
			}()
			researchRunCell(t, &cell, settings)
		})
		cells = append(cells, cell)
		var summaries []map[string]any
		for _, value := range cells {
			summaries = append(summaries, map[string]any{"case_id": value.Case.ID, "order": value.Order, "status": value.Status, "metrics": value.Metrics, "turns": value.Turns, "artifact": value.Case.ID + ".json"})
		}
		researchWriteJSON(t, output, "metrics.json", summaries, redact)
		researchWrite(t, output, "review.md", []byte(researchReview(cells)), redact)
		t.Logf("%s: %s; artifacts saved", item.ID, cell.Status)
	}
	if len(cells) == 0 {
		t.Fatal("no cases selected")
	}
	manifest["finished_at"] = time.Now().UTC()
	manifest["recorded_cases"] = len(cells)
	researchWriteJSON(t, output, "manifest.json", manifest, redact)
}

func researchRunCell(t *testing.T, cell *researchCell, settings config.Config) {
	f := newRuntimeFixture(t)
	observer := &researchObserver{}
	runner := NewRunner(f.pool, settings)
	client := &http.Client{Timeout: 120 * time.Second, Transport: observer}
	runner.httpClient, runner.webTools.httpClient = client, client
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	go runner.Start(ctx)
	for _, text := range cell.Case.Prompts {
		before := researchVisibleMessages(t, f)
		started := time.Now()
		turn := researchTurn{Prompt: text, Status: "incomplete"}
		f.userMessage(text)
		runner.Wake(f.userID)
		for {
			visible := researchVisibleMessages(t, f)
			turn.Messages = visible[len(before):]
			if len(turn.Messages) > 0 {
				first := turn.Messages[0].CreatedAt.Sub(started).Seconds()
				last := turn.Messages[len(turn.Messages)-1].CreatedAt.Sub(started).Seconds()
				turn.FirstVisible, turn.LastVisible = &first, &last
			}
			var pending, unfinished, running int
			queryContext, cancelQuery := context.WithTimeout(context.Background(), 3*time.Second)
			err := f.pool.QueryRow(queryContext, "SELECT (SELECT count(*) FROM messages WHERE sequence IS NULL), (SELECT count(*) FROM steps WHERE finished_at IS NULL), (SELECT count(*) FROM subagents WHERE status='running')").Scan(&pending, &unfinished, &running)
			cancelQuery()
			if err != nil {
				turn.Error = err.Error()
				break
			}
			runner.mutex.Lock()
			active := len(runner.active)
			runner.mutex.Unlock()
			if pending == 0 && unfinished == 0 && running == 0 && active == 0 {
				seconds := time.Since(started).Seconds()
				turn.Quiescence = &seconds
				if len(turn.Messages) == 0 {
					turn.Error = "runtime stopped without a user-visible reply"
				} else {
					turn.Status = "quiescent_unscored"
				}
				break
			}
			select {
			case <-ctx.Done():
				turn.Error = ctx.Err().Error()
			case <-time.After(150 * time.Millisecond):
			}
			if turn.Error != "" {
				break
			}
		}
		cell.Turns = append(cell.Turns, turn)
		if turn.Status != "quiescent_unscored" {
			cell.Errors = append(cell.Errors, turn.Error)
			break
		}
	}
	cancel()
	stopped := time.Now().Add(10 * time.Second)
	for {
		runner.mutex.Lock()
		active := len(runner.active)
		runner.mutex.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(stopped) {
			cell.Errors = append(cell.Errors, "runtime did not stop within cleanup deadline")
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cell.Messages = researchTable(t, f, "messages")
	cell.Steps = researchTable(t, f, "steps")
	cell.Subagents = researchTable(t, f, "subagents")
	observer.mutex.Lock()
	cell.Traces = append([]researchHTTPTrace(nil), observer.traces...)
	observer.mutex.Unlock()
	sort.SliceStable(cell.Traces, func(i, j int) bool { return cell.Traces[i].StartedAt.Before(cell.Traces[j].StartedAt) })
	cell.Metrics = researchMeasure(cell)
	if cell.Case.RequireDelegation && (cell.Metrics.SubagentCount == 0 || cell.Metrics.SubagentReports == 0) {
		cell.Errors = append(cell.Errors, "required delegation did not produce a persisted Subagent report to Main")
	}
	if cell.Metrics.ModelCalls["main"] == 0 {
		cell.Errors = append(cell.Errors, "no Main model request observed")
	}
	for _, trace := range cell.Traces {
		if trace.Actor != "" && (!trace.PromptVerified || (trace.Actor == "main" && trace.MainPromptHash != cell.MainPromptHash)) {
			cell.Errors = append(cell.Errors, "production role prompt not verified")
		}
	}
	cell.Status = "quiescent_unscored"
	if cell.Metrics.HTTPFailures > 0 || cell.Metrics.PersistedErrors > 0 || cell.Metrics.ToolErrors > 0 {
		cell.Status = "quiescent_with_errors_unscored"
	}
	if len(cell.Errors) > 0 {
		cell.Status = "incomplete"
	}
	if cell.Status != "quiescent_unscored" {
		t.Errorf("%s outcome %s; preserved errors: %v", cell.Case.ID, cell.Status, cell.Errors)
	}
}

func researchVisibleMessages(t *testing.T, f *runtimeFixture) []researchVisible {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := f.pool.Query(ctx, "SELECT id::text, content, created_at FROM messages WHERE subagent_id IS NULL AND role='assistant' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var visible []researchVisible
	for rows.Next() {
		var id string
		var content []byte
		var created time.Time
		if err := rows.Scan(&id, &content, &created); err != nil {
			t.Fatal(err)
		}
		for _, message := range MessagesToUser(content) {
			visible = append(visible, researchVisible{MessageID: id, ToolCallID: message.ToolCallID, Text: message.Text, CreatedAt: created})
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return visible
}

func researchTable(t *testing.T, f *runtimeFixture, table string) json.RawMessage {
	t.Helper()
	var body []byte
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.pool.QueryRow(ctx, "SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY r.id), '[]'::jsonb) FROM "+table+" r").Scan(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func researchMeasure(cell *researchCell) researchMetrics {
	metrics := researchMetrics{ModelCalls: map[string]int{}, ToolCalls: map[string]map[string]int{"main": {}, "subagent": {}}, SuccessfulWebCalls: map[string]map[string]int{"main": {}, "subagent": {}}}
	var subagents []json.RawMessage
	json.Unmarshal(cell.Subagents, &subagents)
	metrics.SubagentCount = len(subagents)
	var input, output int64
	cost := decimal.Zero
	for _, trace := range cell.Traces {
		if trace.Error != "" || trace.Status >= 400 {
			metrics.HTTPFailures++
		}
		if trace.Actor == "" {
			continue
		}
		metrics.ModelCalls[trace.Actor]++
		var response struct {
			Usage struct {
				Input  *int64          `json:"prompt_tokens"`
				Output *int64          `json:"completion_tokens"`
				Cost   json.RawMessage `json:"cost"`
			} `json:"usage"`
		}
		if json.Unmarshal(trace.Response, &response) == nil {
			if response.Usage.Input != nil && response.Usage.Output != nil {
				metrics.TokenKnownCalls++
				input += *response.Usage.Input
				output += *response.Usage.Output
			}
			if value := strings.Trim(string(response.Usage.Cost), "\""); value != "" && value != "null" {
				if amount, err := decimal.NewFromString(value); err == nil {
					metrics.CostKnownCalls++
					cost = cost.Add(amount)
				}
			}
		}
	}
	totalCalls := metrics.ModelCalls["main"] + metrics.ModelCalls["subagent"]
	if totalCalls > 0 && metrics.TokenKnownCalls == totalCalls {
		metrics.InputTokens, metrics.OutputTokens = &input, &output
	}
	metrics.KnownModelCost = cost.String()
	if totalCalls > 0 && metrics.CostKnownCalls == totalCalls {
		value := cost.String()
		metrics.ModelCost = &value
	}
	var messages []struct {
		SubagentID *string         `json:"subagent_id"`
		Role       string          `json:"role"`
		Sender     string          `json:"sender"`
		Content    json.RawMessage `json:"content"`
	}
	json.Unmarshal(cell.Messages, &messages)
	type owner struct{ actor, name string }
	calls := map[string]owner{}
	for _, message := range messages {
		if message.SubagentID == nil && message.Sender == "subagent" {
			metrics.SubagentReports++
		}
		contextID, actor := "main", "main"
		if message.SubagentID != nil {
			contextID, actor = *message.SubagentID, "subagent"
		}
		if message.Role == "assistant" {
			var assistant assistantMessage
			json.Unmarshal(message.Content, &assistant)
			for _, call := range assistant.ToolCalls {
				metrics.ToolCalls[actor][call.Function.Name]++
				calls[contextID+":"+call.ID] = owner{actor, call.Function.Name}
			}
			continue
		}
		if message.Role != "tool" {
			continue
		}
		var result struct {
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		}
		json.Unmarshal(message.Content, &result)
		key := contextID + ":" + result.ToolCallID
		call, exists := calls[key]
		delete(calls, key)
		if strings.HasPrefix(result.Content, "Error:") {
			metrics.ToolErrors++
			continue
		}
		if exists && (call.name == "search" || call.name == "fetch_url") && result.Content != "" {
			metrics.SuccessfulWebCalls[call.actor][call.name]++
		}
	}

	var steps []struct {
		Error *string `json:"error"`
	}
	json.Unmarshal(cell.Steps, &steps)
	for _, step := range steps {
		if step.Error != nil {
			metrics.PersistedErrors++
		}
	}
	return metrics
}

func researchReview(cells []researchCell) string {
	var body strings.Builder
	body.WriteString("# Agent 调研回归记录\n\n当前生产方案：Main 和 Subagent 均可使用 search/fetch_url，Main 可自行执行或分派。使用实际生产角色提示词和工具清单；仅本次重放为两种角色指定 temperature 0。完整提示词、配置和源码摘要见 manifest.json。\n\n每题一个样本，完整可见消息包含进度与回复；quiescent_unscored 仅表示运行时空闲且有可见消息，不是质量评分，也不证明最后一条就是最终答案。首条和最后一条可见消息落库耗时分别记录，不包含浏览器/WS。模型费用不包含搜索和读取供应商，缺失费用保留为 null。工具尝试来自 assistant 请求；返回未报错次数按同一 Agent 内的 tool_call_id 匹配实际工具结果，不代表资料质量。原始消息、步骤、Subagent 状态和无请求头的 HTTP 轨迹见各题 JSON。\n\n")
	for _, cell := range cells {
		fmt.Fprintf(&body, "## %s · %s\n\n运行顺序：%d；状态：%s；模型请求 Main/Subagent：%d/%d；Subagent 数量/回传数：%d/%d；Main search/fetch_url 尝试：%d/%d；返回未报错：%d/%d；模型费用：%s USD。\n\n", cell.Case.ID, cell.Case.Title, cell.Order, cell.Status, cell.Metrics.ModelCalls["main"], cell.Metrics.ModelCalls["subagent"], cell.Metrics.SubagentCount, cell.Metrics.SubagentReports, cell.Metrics.ToolCalls["main"]["search"], cell.Metrics.ToolCalls["main"]["fetch_url"], cell.Metrics.SuccessfulWebCalls["main"]["search"], cell.Metrics.SuccessfulWebCalls["main"]["fetch_url"], researchCost(cell.Metrics.ModelCost))
		for index, turn := range cell.Turns {
			fmt.Fprintf(&body, "### 第 %d 轮\n\n用户提示词：\n\n%s\n\n状态：%s；首条可见消息落库：%s 秒；最后一条可见消息落库：%s 秒；运行时空闲：%s 秒。\n\n", index+1, turn.Prompt, turn.Status, researchDuration(turn.FirstVisible), researchDuration(turn.LastVisible), researchDuration(turn.Quiescence))
			for number, message := range turn.Messages {
				fmt.Fprintf(&body, "**Main 消息 %d**\n\n%s\n\n", number+1, message.Text)
			}
			if turn.Error != "" {
				fmt.Fprintf(&body, "执行问题：%s\n\n", turn.Error)
			}
		}
		if len(cell.Errors) > 0 {
			fmt.Fprintf(&body, "保留问题：%s\n\n", strings.Join(cell.Errors, "; "))
		}
		fmt.Fprintf(&body, "[完整记录](%s.json)\n\n", cell.Case.ID)
	}
	return body.String()
}

func researchCost(value *string) string {
	if value == nil {
		return "unknown"
	}
	return *value
}

func researchDuration(value *float64) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.2f", *value)
}

func researchRedactor(settings config.Config) func([]byte) []byte {
	var replacements []string
	for _, secret := range []string{settings.OpenRouterAPIKey, settings.SERPAPIKey, settings.ParallelAPIKey, settings.EveryInfraAPIKey, settings.ResendAPIKey, settings.DatabaseURL, os.Getenv("TEST_DATABASE_URL")} {
		if secret != "" {
			replacements = append(replacements, secret, "[REDACTED]")
		}
	}
	replacer := strings.NewReplacer(replacements...)
	return func(body []byte) []byte { return []byte(replacer.Replace(string(body))) }
}

func researchWriteJSON(t *testing.T, directory, name string, value any, redact func([]byte) []byte) {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	researchWrite(t, directory, name, append(body, '\n'), redact)
}

func researchWrite(t *testing.T, directory, name string, body []byte, redact func([]byte) []byte) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path+".tmp", redact(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

func researchHash(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func researchGit(arguments ...string) string {
	body, err := exec.Command("git", arguments...).CombinedOutput()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	return strings.TrimSpace(string(body))
}

func researchSourceHashes(t *testing.T) map[string]string {
	t.Helper()
	values := map[string]string{}
	paths := []string{"runner.go", "openrouter.go", "prompt.go", "tools.go", "web_tools.go", "delivery.go", "messages_to_user.go", "messages_to_user_test.go", "architecture_tools_test.go", "agent_research_live_test.go", "testdata/agent_research.json", "runner_integration_test.go", "../config/config.go", "../../db/queries/messages.sql"}
	migrations, err := filepath.Glob("../../db/migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, migrations...)
	deliveryTests, err := filepath.Glob("delivery*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, deliveryTests...)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		values[path] = researchHash(body)
	}
	return values
}
