// 本文件负责把数据库消息组装成模型请求，并从消息尾部判断 Main 下一步该做什么：第一条注入固定指令，其后是上一个 compact 以来的原生消息，
// 每组工具回执紧跟它的调用，时间与进度提醒在组装时按历史现算、不落库，所以同一段历史每次组装出的前缀都一样。

package agent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
)

// silentToolRoundsBeforeReminder 是从最近一条用户消息或 Main 的可见正文算起，连续多少轮只调工具后插入一次进度提醒。
const silentToolRoundsBeforeReminder = 4

// parsedContextMessage 是组装上下文时从一条已存消息里读出的共用字段。
type parsedContextMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []toolCall      `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

// parseContextMessage 解析一条已存消息；内容由写入方保证形状，这里只做组装所需的读取。
func parseContextMessage(raw json.RawMessage) parsedContextMessage {
	var message parsedContextMessage
	_ = json.Unmarshal(raw, &message)
	return message
}

// runtimeMessage 生成一条运行时注入的不可见 user 消息，用 name 标明用途；它只存在于请求里，不落库。
func runtimeMessage(name, content string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"role": "user", "name": name, "content": content})
	return raw
}

// requestMessage 把一条已存消息转成请求中的原生消息：只删前端展示字段，正文、工具调用与渠道原生字段原样保留。
func requestMessage(row database.ListAgentContextRowsRow) json.RawMessage {
	var message map[string]json.RawMessage
	if json.Unmarshal(row.Content, &message) != nil {
		return row.Content
	}
	// 界面引用位置和文件名只用于历史展示，不发送给模型供应商。
	delete(message, "user_text")
	delete(message, "quotes")
	delete(message, "image_names")
	delete(message, "parts")
	raw, _ := json.Marshal(message)
	return raw
}

// assembleMessages 按组装规则把当前世代的消息排成请求顺序，普通请求与压缩请求共用：
// 每个工具调用的回执紧跟在它的 assistant 之后（工具执行期间到达的用户消息因此排在回执之后），
// 每条用户发来的消息后接一条当时的时间，连续每满 4 轮没有可见正文的工具调用后插一条进度提醒。
func assembleMessages(rows []database.ListAgentContextRowsRow) []json.RawMessage {
	results := map[string]json.RawMessage{}
	for _, row := range rows {
		if message := parseContextMessage(row.Content); message.Role == "tool" {
			results[message.ToolCallID] = requestMessage(row)
		}
	}
	messages := []json.RawMessage{}
	silentToolRounds := 0
	for _, row := range rows {
		message := parseContextMessage(row.Content)
		switch message.Role {
		case "tool":
			continue
		case "user":
			messages = append(messages, requestMessage(row))
			// 时间取这条消息自己的入库时间，同一条消息每次组装出的时间都一样，不破坏前缀缓存。
			if row.Sender != nil && *row.Sender == "user" {
				messages = append(messages, runtimeMessage(currentTimeMessageName, fmt.Sprintf(currentTimePrompt, row.CreatedAt.UTC().Format(time.RFC3339))))
				silentToolRounds = 0
			}
		case "assistant":
			messages = append(messages, requestMessage(row))
			seen := map[string]bool{}
			for _, call := range message.ToolCalls {
				if result, ok := results[call.ID]; ok && !seen[call.ID] {
					messages = append(messages, result)
				}
				seen[call.ID] = true
			}
			// Main 写了可见正文就重新计数；只有没有正文的工具轮才累计，满 4 轮插一次提醒。
			if AssistantTextToUser(row.Content) != "" {
				silentToolRounds = 0
				continue
			}
			if len(message.ToolCalls) == 0 {
				continue
			}
			silentToolRounds++
			if silentToolRounds == silentToolRoundsBeforeReminder {
				messages = append(messages, runtimeMessage(progressReminderMessageName, progressReminder))
				silentToolRounds = 0
			}
		}
	}
	return messages
}

// buildRequestMessages 组装一次普通请求：第一条是固定指令，其后是按组装规则排好的当前世代消息。
func buildRequestMessages(rows []database.ListAgentContextRowsRow) []json.RawMessage {
	return append([]json.RawMessage{runtimeMessage(instructionsMessageName, MainInstructions)}, assembleMessages(rows)...)
}

// buildAgentRequest 组装一次普通 Agent 请求：组装好的消息发出前先过工具配对与尾巴检查，再附上工具定义与输出上限交给发送层。
func buildAgentRequest(rows []database.ListAgentContextRowsRow, model string, outputLimit int32) (modelRequest, error) {
	messages := buildRequestMessages(rows)
	if err := validateRequestMessages(messages); err != nil {
		return modelRequest{}, err
	}
	// 只取实际组装的这批行的最大序号；摘要排在首位，其物理序号可能大于保留的原文。
	var inputSequence int64
	for _, row := range rows {
		inputSequence = max(inputSequence, row.Sequence)
	}
	// 最近一次用户输入之后还没有可见正文时，工具续接也不能用沉默结束；已经说过话才允许安静收尾。
	requireOutput := true
	for index := len(rows) - 1; index >= 0; index-- {
		row := rows[index]
		role := parseContextMessage(row.Content).Role
		if role == "user" {
			break
		}
		if role == "assistant" && AssistantTextToUser(row.Content) != "" {
			requireOutput = false
			break
		}
	}
	return modelRequest{InputSequence: inputSequence, RequireOutput: requireOutput, Model: model, Messages: messages, Tools: tools.Definitions(), MaxTokens: outputLimit}, nil
}

// buildCompactRequestMessages 组装一次压缩请求：压缩指令作为不可见 user 消息在前，待压历史按同一组装规则居中，末尾一条用户消息提出摘要要求。
func buildCompactRequestMessages(rows []database.ListAgentContextRowsRow, summaryPrompt string) []json.RawMessage {
	compactInstructionsMessage, _ := json.Marshal(map[string]string{"role": "user", "content": compactInstructions})
	summaryRequestMessage, _ := json.Marshal(map[string]string{"role": "user", "content": summaryPrompt})
	messages := append([]json.RawMessage{compactInstructionsMessage}, assembleMessages(rows)...)
	return append(messages, summaryRequestMessage)
}

// validateRequestMessages 是发给模型供应商前的最后一道检查：每个工具调用都有回执，且最后一条是 user 或 tool（有待模型回应的内容）。
func validateRequestMessages(messages []json.RawMessage) error {
	called := map[string]bool{}
	answered := map[string]bool{}
	for _, raw := range messages {
		message := parseContextMessage(raw)
		for _, call := range message.ToolCalls {
			called[call.ID] = true
		}
		if message.Role == "tool" {
			answered[message.ToolCallID] = true
		}
	}
	for id := range called {
		if !answered[id] {
			return fmt.Errorf("model request has a tool call without a result: %s", id)
		}
	}
	last := parseContextMessage(messages[len(messages)-1])
	if last.Role != "user" && last.Role != "tool" {
		return fmt.Errorf("model request must end with a user or tool message, got %q", last.Role)
	}
	return nil
}

// pendingToolCalls 从消息尾部找出最后一条 assistant 里还没有回执的工具调用，连同产生它的步骤一起返回；
// 正常情况下这是刚保存的模型回复，进程中断后重启时则是上次没跑完的那批工具。
func pendingToolCalls(rows []database.ListAgentContextRowsRow) ([]toolCall, uuid.UUID) {
	answered := map[string]bool{}
	for index := len(rows) - 1; index >= 0; index-- {
		message := parseContextMessage(rows[index].Content)
		if message.Role == "tool" {
			answered[message.ToolCallID] = true
			continue
		}
		if message.Role != "assistant" {
			continue
		}
		// 同一调用 ID 重复出现时只执行第一个，避免历史里出现两条同号回执。
		pending := []toolCall{}
		for _, call := range message.ToolCalls {
			if !answered[call.ID] {
				pending = append(pending, call)
				answered[call.ID] = true
			}
		}
		return pending, *rows[index].StepID
	}
	return nil, uuid.Nil
}

// needsReply 判断 Main 是否该调用模型：最后一条消息是用户消息、摘要或工具回执时需要回应，是 assistant 说明已经回复完毕。
func needsReply(rows []database.ListAgentContextRowsRow) bool {
	return len(rows) > 0 && parseContextMessage(rows[len(rows)-1].Content).Role != "assistant"
}
