// 本文件负责 Subagent 的交付检查：确认它结束前最后一次工具调用是成功送达给 Main 的 send_message，没送达时只给一次纠正机会；Main 直接以正文回复用户，不走这项检查。

package agent

import (
	"encoding/json"
	"errors"
)

var errMissingDelivery = errors.New("subagent ended without a final delivered send_message to main")

const deliveryCorrection = `Runtime delivery check: you tried to end your turn, but your last tool call was not a successfully delivered send_message to Main. Call send_message now with to=main: send the current result, a concrete blocker, or a concise update if waiting for background work. Do not disclose private reasoning or repeat already delivered results. This is the only delivery correction for this attempt to end the turn.`

type deliveryState struct {
	satisfied bool
	corrected bool
}

// 已提交的工具回执决定 Subagent 是否交付；新输入和后续工作不能复用旧的交付记录。Main 以正文回复用户，恒视为无需检查。
func inspectDelivery(messages []json.RawMessage, address agentAddress) deliveryState {
	if address.subagentID == nil {
		return deliveryState{satisfied: true}
	}
	var state deliveryState
	calls := map[string]toolCall{}
	lastToolID := ""
	for _, raw := range messages {
		var message struct {
			Role              string          `json:"role"`
			Name              string          `json:"name"`
			Content           json.RawMessage `json:"content"`
			ToolCalls         []toolCall      `json:"tool_calls"`
			ToolCallID        string          `json:"tool_call_id"`
			DeliverySatisfied bool            `json:"delivery_satisfied"`
			DeliveryCorrected bool            `json:"delivery_corrected"`
		}
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		var text string
		json.Unmarshal(message.Content, &text)
		switch message.Role {
		case "user":
			if message.Name == "context_summary" {
				state = deliveryState{satisfied: message.DeliverySatisfied, corrected: message.DeliveryCorrected}
				clear(calls)
				continue
			}
			state = deliveryState{}
			clear(calls)
		case "system":
			if text == deliveryCorrection {
				state.corrected = true
			}
		case "assistant":
			clear(calls)
			if len(message.ToolCalls) > 0 {
				state.satisfied = false
				lastToolID = message.ToolCalls[len(message.ToolCalls)-1].ID
			}
			for _, call := range message.ToolCalls {
				calls[call.ID] = call
			}
		case "tool":
			call, ok := calls[message.ToolCallID]
			if !ok {
				continue
			}
			delete(calls, message.ToolCallID)
			if call.ID != lastToolID || call.Function.Name != "send_message" || text != "delivered" {
				continue
			}
			arguments, err := parseSendMessage(call)
			if err != nil {
				continue
			}
			state.satisfied = arguments.To == "main"
			if state.satisfied {
				state.corrected = false
			}
		}
	}
	return state
}
