package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
)

func writeDeliveryReply(w http.ResponseWriter, text string, calls ...toolCall) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text, "tool_calls": calls}}},
		"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 3, "cost": "0.125"},
	})
}

func deliveryUserTexts(t *testing.T, f *runtimeFixture) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), "SELECT content FROM messages WHERE subagent_id IS NULL AND role='assistant' ORDER BY sequence")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var texts []string
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		for _, message := range MessagesToUser(raw) {
			texts = append(texts, message.Text)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return texts
}

func assertDeliveryAccounting(t *testing.T, f *runtimeFixture, requests int) {
	t.Helper()
	if got := f.count("SELECT count(*) FROM steps WHERE finished_at IS NOT NULL AND input_tokens=7 AND output_tokens=3 AND cost=0.125"); got != requests {
		t.Fatalf("accounted requests=%d, want %d", got, requests)
	}
	if got := f.count("SELECT count(*) FROM steps WHERE finished_at IS NULL"); got != 0 {
		t.Fatalf("unfinished steps=%d", got)
	}
}

func TestDeliveryRepairsPrivateFinalAndAllowsAcknowledgement(t *testing.T) {
	for _, sub := range []bool{false, true} {
		t.Run(fmt.Sprintf("sub=%t", sub), func(t *testing.T) {
			f := newRuntimeFixture(t)
			address := agentAddress{userID: f.userID}
			target := "user"
			if sub {
				id := f.subagent("research")
				address.subagentID = &id
				target = "main"
			} else {
				f.userMessage("research")
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				model := decodeRuntimeModel(t, w, request)
				switch requests.Add(1) {
				case 1:
					writeDeliveryReply(w, "PRIVATE DRAFT must never leak")
				case 2:
					if model.lastRole() != "system" || model.lastText() != deliveryCorrection {
						t.Error("correction was not included in real model context")
					}
					writeDeliveryReply(w, "", runtimeTool("send_message", map[string]string{"to": target, "text": "public final"}))
				case 3:
					writeDeliveryReply(w, "Sent; private acknowledgement")
				default:
					t.Error("unexpected duplicate request")
					writeDeliveryReply(w, "")
				}
			}))
			defer server.Close()
			if !newRuntimeRunner(f.pool, server).run(context.Background(), address) {
				t.Fatal("actor did not run")
			}
			if requests.Load() != 3 {
				t.Fatalf("requests=%d", requests.Load())
			}
			assertDeliveryAccounting(t, f, 3)
			if f.count("SELECT count(*) FROM steps WHERE error=$1", errMissingDelivery.Error()) != 1 {
				t.Error("invalid successful response lost its delivery error")
			}
			if sub {
				if f.count("SELECT count(*) FROM messages WHERE subagent_id IS NULL AND sender='subagent' AND content->>'content' LIKE '%public final%'") != 1 {
					t.Error("Sub result was not reported exactly once")
				}
				if f.count("SELECT count(*) FROM subagents WHERE status='idle'") != 1 {
					t.Error("reported Sub did not become idle")
				}
			} else if texts := deliveryUserTexts(t, f); len(texts) != 1 || texts[0] != "public final" {
				t.Fatalf("visible texts=%v", texts)
			}
		})
	}
}

func TestDeliveryFailureIsBoundedAndCommitted(t *testing.T) {
	for _, sub := range []bool{false, true} {
		t.Run(fmt.Sprintf("sub=%t", sub), func(t *testing.T) {
			f := newRuntimeFixture(t)
			address := agentAddress{userID: f.userID}
			if sub {
				id := f.subagent("research")
				address.subagentID = &id
			} else {
				f.userMessage("research")
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				writeDeliveryReply(w, "PRIVATE unreported final")
			}))
			defer server.Close()
			newRuntimeRunner(f.pool, server).run(context.Background(), address)
			if requests.Load() != 2 {
				t.Fatalf("requests=%d, want bounded 2", requests.Load())
			}
			assertDeliveryAccounting(t, f, 2)
			if f.count("SELECT count(*) FROM steps WHERE error=$1", errMissingDelivery.Error()) != 2 {
				t.Error("invalid response usages/errors were rolled back")
			}
			if f.count("SELECT count(*) FROM messages WHERE role='system' AND content->>'content'=$1", deliveryCorrection) != 1 {
				t.Error("correction marker missing or repeated")
			}
			if len(deliveryUserTexts(t, f)) != 0 {
				t.Error("private output became visible")
			}
			if sub {
				if f.count("SELECT count(*) FROM subagents WHERE status='failed'") != 1 {
					t.Error("terminal Sub was not failed atomically")
				}
				if f.count("SELECT count(*) FROM messages WHERE sender='system' AND subagent_id IS NULL") != 1 {
					t.Error("missing or duplicate Main failure notification")
				}
			}
		})
	}
}

func TestDeliveryProgressThenWorkRequiresFinal(t *testing.T) {
	f := newRuntimeFixture(t)
	f.userMessage("research")
	var requests, searches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/search" {
			searches.Add(1)
			fmt.Fprint(w, `{"organic":[]}`)
			return
		}
		model := decodeRuntimeModel(t, w, request)
		switch requests.Add(1) {
		case 1:
			writeDeliveryReply(w, "", runtimeTool("send_message", map[string]string{"to": "user", "text": "Checking the sources."}))
		case 2:
			writeDeliveryReply(w, "", runtimeTool("search", map[string]string{"query": "fact"}))
		case 3:
			writeDeliveryReply(w, "PRIVATE actual final")
		case 4:
			if model.lastText() != deliveryCorrection {
				t.Error("work reused progress delivery credit")
			}
			writeDeliveryReply(w, "", runtimeTool("send_message", map[string]string{"to": "user", "text": "Final result."}))
		case 5:
			writeDeliveryReply(w, "Sent.")
		default:
			t.Error("unexpected request")
			writeDeliveryReply(w, "")
		}
	}))
	defer server.Close()
	newRuntimeRunner(f.pool, server).run(context.Background(), agentAddress{userID: f.userID})
	assertDeliveryAccounting(t, f, 5)
	if searches.Load() != 1 {
		t.Fatalf("searches=%d", searches.Load())
	}
	if texts := deliveryUserTexts(t, f); len(texts) != 2 || texts[0] != "Checking the sources." || texts[1] != "Final result." {
		t.Fatalf("visible texts=%v", texts)
	}
}

func TestDeliveryRestartRetainsCorrectionBudget(t *testing.T) {
	f := newRuntimeFixture(t)
	f.userMessage("research")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writeDeliveryReply(w, "PRIVATE never delivered")
	}))
	defer server.Close()
	address := agentAddress{userID: f.userID}
	first := newRuntimeRunner(f.pool, server)
	connection, err := f.pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	step, err := first.prepareStep(context.Background(), connection, address)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := first.callWithRetries(context.Background(), connection, address, step)
	if err != nil {
		t.Fatal(err)
	}
	var message assistantMessage
	if err := json.Unmarshal(reply.response.Choices[0].Message, &message); err != nil {
		t.Fatal(err)
	}
	if err := first.saveReply(context.Background(), connection, address, reply.stepID, reply.response, message, nil, reply.delivery); err != nil {
		t.Fatal(err)
	}
	connection.Release()
	if f.count("SELECT count(*) FROM steps WHERE finished_at IS NULL") != 1 {
		t.Fatal("correction continuation was not durable")
	}
	newRuntimeRunner(f.secondPool(), server).run(context.Background(), address)
	if requests.Load() != 2 {
		t.Fatalf("restart reset correction budget: requests=%d", requests.Load())
	}
	assertDeliveryAccounting(t, f, 2)
}

func TestDeliveryNewInputResetsPreviousDeliveryAndCorrection(t *testing.T) {
	f := newRuntimeFixture(t)
	f.userMessage("first")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		model := decodeRuntimeModel(t, w, request)
		switch requests.Add(1) {
		case 1, 4:
			writeDeliveryReply(w, "PRIVATE final")
		case 2, 5:
			if model.lastText() != deliveryCorrection {
				t.Error("new input did not receive its own correction")
			}
			writeDeliveryReply(w, "", runtimeTool("send_message", map[string]string{"to": "user", "text": "delivered"}))
		case 3, 6:
			writeDeliveryReply(w, "ack")
		default:
			t.Error("unexpected request")
			writeDeliveryReply(w, "")
		}
	}))
	defer server.Close()
	runner := newRuntimeRunner(f.pool, server)
	address := agentAddress{userID: f.userID}
	runner.run(context.Background(), address)
	f.userMessage("second")
	runner.run(context.Background(), address)
	assertDeliveryAccounting(t, f, 6)
	if f.count("SELECT count(*) FROM messages WHERE role='system' AND content->>'content'=$1", deliveryCorrection) != 2 {
		t.Error("new input failed to reset bounded correction")
	}
	if len(deliveryUserTexts(t, f)) != 2 {
		t.Error("new input lost response or duplicated it")
	}
}

func TestDeliveryFailurePreservesPendingFollowupAndNotificationOnRestart(t *testing.T) {
	f := newRuntimeFixture(t)
	id := f.subagent("first task")
	address := agentAddress{userID: f.userID, subagentID: &id}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch requests.Add(1) {
		case 1:
			writeDeliveryReply(w, "PRIVATE first")
		case 2:
			if err := deliverText(context.Background(), database.New(f.pool), address, "main", nil, "new followup"); err != nil {
				t.Error(err)
			}
			writeDeliveryReply(w, "PRIVATE invalid correction")
		case 3:
			writeDeliveryReply(w, "", runtimeTool("send_message", map[string]string{"to": "main", "text": "followup done"}))
		case 4:
			writeDeliveryReply(w, "")
		default:
			t.Error("unexpected request")
			writeDeliveryReply(w, "")
		}
	}))
	defer server.Close()
	newRuntimeRunner(f.pool, server).run(context.Background(), address)
	if f.count("SELECT count(*) FROM messages WHERE subagent_id=$1 AND sequence IS NULL", id) != 1 {
		t.Fatal("contract failure consumed an unseen followup")
	}
	if f.count("SELECT count(*) FROM messages WHERE sender='system' AND subagent_id IS NULL") != 1 {
		t.Fatal("failure notification not committed with terminal response")
	}
	if f.count("SELECT count(*) FROM subagents WHERE status='failed'") != 1 {
		t.Fatal("failure status not committed")
	}
	newRuntimeRunner(f.secondPool(), server).run(context.Background(), address)
	assertDeliveryAccounting(t, f, 4)
	if f.count("SELECT count(*) FROM messages WHERE sender='system' AND subagent_id IS NULL") != 1 {
		t.Fatal("restart duplicated terminal notification")
	}
	if f.count("SELECT count(*) FROM messages WHERE sender='subagent' AND content->>'content' LIKE '%followup done%'") != 1 {
		t.Fatal("unseen followup did not reactivate after restart")
	}
	if f.count("SELECT count(*) FROM subagents WHERE status='idle'") != 1 {
		t.Fatal("reactivated Sub did not idle")
	}
}

func TestDeliveryCorrectionHTTPRetryPreservesBudgetAndAccounting(t *testing.T) {
	f := newRuntimeFixture(t)
	f.userMessage("research")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		model := decodeRuntimeModel(t, w, request)
		switch requests.Add(1) {
		case 1:
			writeDeliveryReply(w, "PRIVATE final")
		case 2:
			if model.lastText() != deliveryCorrection {
				t.Error("correction missing from first correction attempt")
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "temporary outage")
		case 3:
			if model.lastText() != deliveryCorrection {
				t.Error("HTTP retry lost durable correction marker")
			}
			writeDeliveryReply(w, "PRIVATE still invalid after retry")
		default:
			t.Error("HTTP retry reset bounded correction")
			writeDeliveryReply(w, "")
		}
	}))
	defer server.Close()
	newRuntimeRunner(f.pool, server).run(context.Background(), agentAddress{userID: f.userID})
	if requests.Load() != 3 {
		t.Fatalf("actual API requests=%d", requests.Load())
	}
	assertDeliveryAccounting(t, f, 2)
	if f.count("SELECT count(*) FROM steps") != 3 {
		t.Fatal("HTTP retry attempt did not get its own step")
	}
	if f.count("SELECT count(*) FROM steps WHERE error LIKE 'openrouter returned 503:%' AND input_tokens IS NULL AND cost IS NULL") != 1 {
		t.Fatal("HTTP failure bookkeeping was fabricated or lost")
	}
	if f.count("SELECT count(*) FROM messages WHERE role='system' AND content->>'content'=$1", deliveryCorrection) != 1 {
		t.Fatal("HTTP retry reset correction allowance")
	}
	if len(deliveryUserTexts(t, f)) != 0 {
		t.Fatal("failed correction leaked private output")
	}
}
