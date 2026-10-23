package agent

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func deliveryContext(role string, content any, calls ...toolCall) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"role": role, "content": content, "tool_calls": calls})
	return raw
}

func deliveryToolResult(call toolCall, result string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"role": "tool", "content": result, "tool_call_id": call.ID})
	return raw
}

func TestDeliveryCreditsOnlySuccessfulRoleCommunication(t *testing.T) {
	id := uuid.New()
	main := agentAddress{userID: uuid.New()}
	sub := agentAddress{userID: main.userID, subagentID: &id}
	for _, test := range []struct {
		name             string
		address          agentAddress
		to, text, result string
		want             bool
	}{
		{"Main user", main, "user", "hello", "delivered", true},
		{"Main followup waiting", main, id.String(), "continue", "delivered", true},
		{"Sub report", sub, "main", "result", "delivered", true},
		{"Sub user forbidden", sub, "user", "secret", "Error: subagents can only message main", false},
		{"Main wrong target", main, "main", "hello", "Error: invalid recipient", false},
		{"cross user", main, id.String(), "hello", "Error: subagent does not belong to this user", false},
		{"blank body", main, "user", " ", "delivered", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := runtimeTool("send_message", map[string]string{"to": test.to, "text": test.text})
			messages := []json.RawMessage{deliveryContext("user", "request"), deliveryContext("assistant", "", call), deliveryToolResult(call, test.result)}
			if got := inspectDelivery(messages, test.address); got.satisfied != test.want {
				t.Fatalf("state=%+v, want satisfied %t", got, test.want)
			}
		})
	}
}

func TestDeliveryWaitingWorkOrderAndCorrectionEpoch(t *testing.T) {
	address := agentAddress{userID: uuid.New()}
	send := runtimeTool("send_message", map[string]string{"to": "user", "text": "progress"})
	search := runtimeTool("search", map[string]string{"query": "fact"})
	assign := runtimeTool("assign_subagents", map[string]any{"tasks": []string{"research"}})
	ids, _ := json.Marshal(map[string]any{"subagent_ids": []uuid.UUID{uuid.New()}})
	base := []json.RawMessage{deliveryContext("user", "request"), deliveryContext("assistant", "", send), deliveryToolResult(send, "delivered"), deliveryContext("system", deliveryCorrection)}
	if got := inspectDelivery(base, address); !got.satisfied || !got.corrected {
		t.Fatalf("base=%+v", got)
	}
	work := append(append([]json.RawMessage{}, base...), deliveryContext("assistant", "", search), deliveryToolResult(search, "Error: timeout"))
	if got := inspectDelivery(work, address); got.satisfied || !got.corrected {
		t.Fatalf("work must clear credit but retain correction budget: %+v", got)
	}
	waiting := append(append([]json.RawMessage{}, work...), deliveryContext("assistant", "", assign), deliveryToolResult(assign, string(ids)))
	if got := inspectDelivery(waiting, address); !got.satisfied || !got.corrected {
		t.Fatalf("successful delegation did not allow waiting: %+v", got)
	}
	input := append(waiting, deliveryContext("user", "new input"))
	if got := inspectDelivery(input, address); got.satisfied || got.corrected {
		t.Fatalf("new input did not reset both: %+v", got)
	}
	if got := inspectDelivery([]json.RawMessage{deliveryContext("assistant", "", send), deliveryToolResult(search, "delivered")}, address); got.satisfied {
		t.Fatal("unmatched tool receipt counted")
	}
	malformed := send
	malformed.Function.Arguments = "not JSON"
	if got := inspectDelivery([]json.RawMessage{deliveryContext("assistant", "", malformed), deliveryToolResult(malformed, "delivered")}, address); got.satisfied {
		t.Fatal("malformed send counted")
	}
}

func TestDeliveryPrivateContentBoundary(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{`null`, false}, {`""`, false}, {`" \n "`, false}, {`"private result"`, true},
		{`[{"type":"text","text":"result"}]`, true}, {`[{"type":"text","text":"  "}]`, false},
	} {
		if got := hasPrivateText(json.RawMessage(test.raw)); got != test.want {
			t.Errorf("%s: got %t", test.raw, got)
		}
	}
}
