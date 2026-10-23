package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
)

func TestArchitectureToolSchemas(t *testing.T) {
	subagentID := uuid.New()
	for _, test := range []struct {
		name       string
		address    agentAddress
		wantTools  []json.RawMessage
		wantNames  []string
		wantPrompt string
	}{
		{"Main", agentAddress{}, mainTools, []string{"send_message", "assign_subagents", "search", "fetch_url"}, mainPrompt},
		{"Subagent", agentAddress{subagentID: &subagentID}, subagentTools, []string{"send_message", "search", "fetch_url"}, subagentPrompt},
	} {
		t.Run(test.name, func(t *testing.T) {
			var advertised struct {
				Tools    []json.RawMessage `json:"tools"`
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			runner := &Runner{
				settings: config.Config{MainModel: "main-test", SubagentModel: "sub-test"},
				httpClient: &http.Client{Transport: runtimeTransport(func(request *http.Request) (*http.Response, error) {
					if err := json.NewDecoder(request.Body).Decode(&advertised); err != nil {
						return nil, err
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":""}}]}`)),
					}, nil
				})},
			}
			if _, _, err := runner.callModel(context.Background(), test.address, nil); err != nil {
				t.Fatal(err)
			}
			schemas := architectureToolSchemas(t, advertised.Tools)
			if !reflect.DeepEqual(schemas, architectureToolSchemas(t, test.wantTools)) {
				t.Fatalf("advertised tool schemas differ from role contract: %s", advertised.Tools)
			}
			if len(schemas) != len(test.wantNames) {
				t.Fatalf("advertised %d tools, want %d", len(schemas), len(test.wantNames))
			}
			for _, name := range test.wantNames {
				if _, exists := schemas[name]; !exists {
					t.Fatalf("missing advertised tool %s", name)
				}
			}
			if len(advertised.Messages) != 1 || advertised.Messages[0].Content != test.wantPrompt {
				t.Fatal("advertised production role prompt differs from its contract")
			}
			if test.address.subagentID != nil {
				for _, call := range []toolCall{
					runtimeTool("assign_subagents", map[string]any{"tasks": []string{"nested task"}}),
					runtimeTool("send_message", map[string]string{"to": "user", "text": "direct message"}),
				} {
					result, err := runner.executeDatabaseTool(context.Background(), nil, test.address, call)
					if err != nil || !strings.HasPrefix(result, "Error:") {
						t.Fatalf("Subagent role restriction was lost for %s: %q, %v", call.Function.Name, result, err)
					}
				}
			}
		})
	}
}

func architectureToolSchemas(t *testing.T, tools []json.RawMessage) map[string]any {
	t.Helper()
	result := make(map[string]any, len(tools))
	for _, tool := range tools {
		var schema map[string]any
		if err := json.Unmarshal(tool, &schema); err != nil {
			t.Fatal(err)
		}
		name := schema["function"].(map[string]any)["name"].(string)
		if _, duplicate := result[name]; duplicate {
			t.Fatalf("tool %s advertised twice", name)
		}
		result[name] = schema
	}
	return result
}

func TestMainExecutesWebToolsByDefault(t *testing.T) {
	for _, toolName := range []string{"search", "fetch_url"} {
		t.Run(toolName, func(t *testing.T) {
			fixture := newRuntimeFixture(t)
			var outbound atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/search":
					outbound.Add(1)
					fmt.Fprint(w, `{"organic":[{"title":"Permission reference","link":"https://example.com/reference","snippet":"Verified search result"}]}`)
					return
				case "/v1/extract":
					outbound.Add(1)
					fmt.Fprint(w, `{"results":[{"full_content":"Verified webpage text","excerpts":[]}],"errors":[]}`)
					return
				}
				model := decodeRuntimeModel(t, w, request)
				switch {
				case model.lastRole() == "user":
					arguments := map[string]string{"query": "permission reference"}
					if toolName == "fetch_url" {
						arguments = map[string]string{"url": "https://example.com/reference"}
					}
					writeRuntimeReply(w, runtimeTool(toolName, arguments))
				case model.lastText() == "delivered":
					writeRuntimeReply(w)
				default:
					writeRuntimeReply(w, runtimeTool("send_message", map[string]string{"to": "user", "text": "Main result: " + model.lastText()}))
				}
			}))
			t.Cleanup(server.Close)
			runner := newRuntimeRunner(fixture.pool, server)
			startRuntime(t, runner)
			fixture.userMessage("probe Main web permission")
			runner.Wake(fixture.userID)
			waitForRuntime(t, "Main emits user-visible tool result", func() bool {
				return fixture.count("SELECT count(*) FROM messages WHERE subagent_id IS NULL AND role='assistant' AND content::text LIKE '%Main result:%'") == 1
			})
			waitForRuntime(t, "Main reaches quiescence", func() bool {
				if fixture.count("SELECT count(*) FROM steps WHERE finished_at IS NULL") != 0 || fixture.count("SELECT count(*) FROM messages WHERE sequence IS NULL") != 0 {
					return false
				}
				runner.mutex.Lock()
				defer runner.mutex.Unlock()
				return len(runner.active) == 0
			})
			if outbound.Load() != 1 {
				t.Fatalf("outbound provider calls = %d, want 1", outbound.Load())
			}
			var result string
			if err := fixture.pool.QueryRow(context.Background(), "SELECT content->>'content' FROM messages WHERE role='tool' AND content->>'content'<>'delivered' ORDER BY sequence LIMIT 1").Scan(&result); err != nil {
				t.Fatal(err)
			}
			wantText := "Verified search result"
			if toolName == "fetch_url" {
				wantText = "Verified webpage text"
			}
			if !strings.Contains(result, wantText) {
				t.Fatalf("Main did not receive actual provider result: %q", result)
			}
			rows, err := database.New(fixture.pool).ListChatRowsAfter(context.Background(), database.ListChatRowsAfterParams{UserID: fixture.userID, ID: uuid.Nil})
			if err != nil {
				t.Fatal(err)
			}
			var visible []MessageToUser
			for _, row := range rows {
				if row.Role == "assistant" {
					visible = append(visible, MessagesToUser(row.Content)...)
				}
			}
			if len(visible) != 1 || visible[0].Text != "Main result: "+result {
				t.Fatalf("unexpected user-visible Main answer: %#v", visible)
			}
			if fixture.count("SELECT count(*) FROM subagents") != 0 || fixture.count("SELECT count(*) FROM steps WHERE error IS NOT NULL") != 0 {
				t.Fatal("Main direct tool probe unexpectedly delegated or failed its model step")
			}
		})
	}
}

func TestArchitectureEvidenceUsage(t *testing.T) {
	for _, test := range []struct {
		name          string
		responses     []string
		costKnown     bool
		knownCost     string
		costCalls     int
		tokensKnown   bool
		tokenCalls    int
		input, output int64
	}{
		{"explicit zero is known", []string{`{"usage":{"prompt_tokens":0,"completion_tokens":0,"cost":0}}`}, true, "0", 1, true, 1, 0, 0},
		{"missing cost does not become zero", []string{`{"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":0}}`, `{"usage":{"prompt_tokens":3,"completion_tokens":4}}`}, false, "0", 1, true, 2, 4, 6},
		{"partial decimal cost remains accurate", []string{`{"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":"0.0123"}}`, `{"usage":{"prompt_tokens":3,"completion_tokens":4,"cost":0.0007}}`, `{"usage":{"prompt_tokens":5,"completion_tokens":6,"cost":null}}`}, false, "0.013", 2, true, 3, 9, 12},
		{"missing token usage stays unknown", []string{`{"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":0}}`, `{"usage":{"prompt_tokens":3,"cost":0}}`}, true, "0", 2, false, 1, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			cell := researchCell{}
			for _, response := range test.responses {
				cell.Traces = append(cell.Traces, researchHTTPTrace{Actor: "main", Status: http.StatusOK, Response: json.RawMessage(response)})
			}
			metrics := researchMeasure(&cell)
			if metrics.ModelCalls["main"] != len(test.responses) || metrics.CostKnownCalls != test.costCalls || metrics.TokenKnownCalls != test.tokenCalls {
				t.Fatalf("incorrect evidence counts: %#v", metrics)
			}
			if metrics.KnownModelCost != test.knownCost {
				t.Fatalf("reported partial model cost = %s, want %s", metrics.KnownModelCost, test.knownCost)
			}
			if test.costKnown {
				if metrics.ModelCost == nil || *metrics.ModelCost != test.knownCost {
					t.Fatalf("known model cost = %v, want %s", metrics.ModelCost, test.knownCost)
				}
			} else if metrics.ModelCost != nil {
				t.Fatalf("incomplete model cost presented as known: %s", *metrics.ModelCost)
			}
			if test.tokensKnown {
				if metrics.InputTokens == nil || metrics.OutputTokens == nil || *metrics.InputTokens != test.input || *metrics.OutputTokens != test.output {
					t.Fatalf("incorrect known token totals: %#v", metrics)
				}
			} else if metrics.InputTokens != nil || metrics.OutputTokens != nil {
				t.Fatal("incomplete token usage presented as a known total")
			}
			if metrics.SupplierCost != nil {
				t.Fatal("model usage must not imply known web supplier billing")
			}
		})
	}
}

func TestArchitectureEvidenceSuccessfulWebCalls(t *testing.T) {
	for _, test := range []struct {
		name            string
		messages        string
		searchAttempts  int
		fetchAttempts   int
		searchSuccesses int
		fetchSuccesses  int
		toolErrors      int
	}{
		{
			name: "failed Main search is not execution",
			messages: `[
				{"role":"assistant","content":{"tool_calls":[{"id":"call1","function":{"name":"search","arguments":"{}"}}]}},
				{"role":"tool","content":{"tool_call_id":"call1","content":"Error: search failed"}}
			]`,
			searchAttempts: 1, toolErrors: 1,
		},
		{
			name: "successful Main fetch has persisted result",
			messages: `[
				{"role":"assistant","content":{"tool_calls":[{"id":"call1","function":{"name":"fetch_url","arguments":"{}"}}]}},
				{"role":"tool","content":{"tool_call_id":"call1","content":"Verified source content"}}
			]`,
			fetchAttempts: 1, fetchSuccesses: 1,
		},
		{
			name:           "pending request is not execution",
			messages:       `[{"role":"assistant","content":{"tool_calls":[{"id":"call1","function":{"name":"search","arguments":"{}"}}]}}]`,
			searchAttempts: 1,
		},
		{
			name:     "unmatched tool result is not execution",
			messages: `[{"role":"tool","content":{"tool_call_id":"missing","content":"Verified source content"}}]`,
		},
		{
			name: "reused call ID matches sequential result",
			messages: `[
				{"role":"assistant","content":{"tool_calls":[{"id":"call1","function":{"name":"search","arguments":"{}"}}]}},
				{"role":"tool","content":{"tool_call_id":"call1","content":"Error: search failed"}},
				{"role":"assistant","content":{"tool_calls":[{"id":"call1","function":{"name":"fetch_url","arguments":"{}"}}]}},
				{"role":"tool","content":{"tool_call_id":"call1","content":"Verified source content"}}
			]`,
			searchAttempts: 1, fetchAttempts: 1, fetchSuccesses: 1, toolErrors: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			metrics := researchMeasure(&researchCell{Messages: json.RawMessage(test.messages)})
			if metrics.ToolCalls["main"]["search"] != test.searchAttempts || metrics.ToolCalls["main"]["fetch_url"] != test.fetchAttempts {
				t.Fatalf("incorrect attempted calls: %#v", metrics.ToolCalls)
			}
			if metrics.SuccessfulWebCalls["main"]["search"] != test.searchSuccesses || metrics.SuccessfulWebCalls["main"]["fetch_url"] != test.fetchSuccesses {
				t.Fatalf("incorrect successful executions: %#v", metrics.SuccessfulWebCalls)
			}
			if metrics.ToolErrors != test.toolErrors {
				t.Fatalf("tool errors = %d, want %d", metrics.ToolErrors, test.toolErrors)
			}
		})
	}
}

func TestArchitectureEvidenceMatchesToolResultsWithinActor(t *testing.T) {
	metrics := researchMeasure(&researchCell{Messages: json.RawMessage(`[
		{"role":"assistant","content":{"tool_calls":[{"id":"shared-id","function":{"name":"search","arguments":"{}"}}]}},
		{"subagent_id":"00000000-0000-0000-0000-000000000001","role":"assistant","content":{"tool_calls":[{"id":"shared-id","function":{"name":"fetch_url","arguments":"{}"}}]}},
		{"subagent_id":"00000000-0000-0000-0000-000000000001","role":"tool","content":{"tool_call_id":"shared-id","content":"Verified source content"}}
	]`)})
	if metrics.ToolCalls["main"]["search"] != 1 || metrics.SuccessfulWebCalls["main"]["search"] != 0 {
		t.Fatalf("Subagent result was misattributed to pending Main call: %#v", metrics)
	}
	if metrics.ToolCalls["subagent"]["fetch_url"] != 1 || metrics.SuccessfulWebCalls["subagent"]["fetch_url"] != 1 {
		t.Fatalf("Subagent execution evidence was lost: %#v", metrics)
	}
}
