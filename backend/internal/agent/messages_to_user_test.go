package agent

import (
	"encoding/json"
	"testing"
)

func TestMessagesToUserExcludesInternalAndInvalidMessages(t *testing.T) {
	calls := []toolCall{
		runtimeTool("assign_subagents", map[string]any{"tasks": []string{"private task"}}),
		runtimeTool("send_message", map[string]string{"to": "main", "text": "private report"}),
		runtimeTool("send_message", map[string]string{"to": "01900000-0000-7000-8000-000000000001", "text": "private instruction"}),
		runtimeTool("send_message", map[string]string{"to": "user", "text": "   "}),
		runtimeTool("search", map[string]string{"to": "user", "text": "not a message"}),
		runtimeTool("send_message", map[string]string{"to": "user", "text": "visible answer"}),
	}
	malformed := runtimeTool("send_message", nil)
	malformed.Function.Arguments = "not JSON"
	calls = append(calls, malformed)
	body, err := json.Marshal(map[string]any{"role": "assistant", "content": "private reasoning", "tool_calls": calls})
	if err != nil {
		t.Fatal(err)
	}
	visible := MessagesToUser(body)
	if len(visible) != 1 || visible[0].Text != "visible answer" || visible[0].ToolCallID != calls[5].ID {
		t.Fatalf("unexpected user-visible messages: %#v", visible)
	}
}
