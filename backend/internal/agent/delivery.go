package agent

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
)

var errMissingDelivery = errors.New("assistant returned private terminal text without send_message")

const deliveryCorrection = `Runtime delivery check: your preceding response contained private text but did not communicate the current result. Private assistant text is never forwarded. Send the useful result or concrete blocker through send_message: to=user for Main, to=main for a Subagent. Do not repeat messages already successfully delivered or disclose private reasoning. If Main is genuinely waiting for background work, yield with empty content. This is the only delivery correction for this incoming message.`

type deliveryState struct {
	satisfied bool
	corrected bool
}

// 已提交的工具回执决定是否交付；新输入和后续工作不能复用旧的交付记录。
func inspectDelivery(messages []json.RawMessage, address agentAddress) deliveryState {
	var state deliveryState
	calls := map[string]toolCall{}
	for _, raw := range messages {
		var message struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCalls  []toolCall      `json:"tool_calls"`
			ToolCallID string          `json:"tool_call_id"`
		}
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		var text string
		json.Unmarshal(message.Content, &text)
		switch message.Role {
		case "user":
			state = deliveryState{}
			clear(calls)
		case "system":
			if text == deliveryCorrection {
				state.corrected = true
			}
		case "assistant":
			clear(calls)
			for _, call := range message.ToolCalls {
				calls[call.ID] = call
			}
		case "tool":
			call, ok := calls[message.ToolCallID]
			if !ok {
				continue
			}
			delete(calls, message.ToolCallID)
			switch call.Function.Name {
			case "search", "fetch_url":
				state.satisfied = false
			case "send_message":
				arguments, err := parseSendMessage(call)
				if err != nil || text != "delivered" {
					continue
				}
				if address.subagentID != nil {
					state.satisfied = arguments.To == "main" || state.satisfied
				} else if arguments.To == "user" {
					state.satisfied = true
				} else if _, err := uuid.Parse(arguments.To); err == nil {
					state.satisfied = true // Main 已发送后续任务，可以等待回报。
				}
			case "assign_subagents":
				var result struct {
					SubagentIDs []uuid.UUID `json:"subagent_ids"`
				}
				if address.subagentID == nil && json.Unmarshal([]byte(text), &result) == nil && len(result.SubagentIDs) > 0 {
					state.satisfied = true
				}
			}
		}
	}
	return state
}

func hasPrivateText(content json.RawMessage) bool {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return strings.TrimSpace(text) != ""
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		for _, block := range blocks {
			if strings.TrimSpace(block.Text) != "" {
				return true
			}
		}
	}
	return false
}
