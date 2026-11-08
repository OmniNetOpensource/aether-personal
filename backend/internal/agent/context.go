// 本文件负责把数据库消息组装成模型请求并配对工具回执：第一条注入固定指令，其后读取摘要与原生消息，记忆通过工具渐进加载；
// 每组工具回执紧跟调用，时间与进度提醒按历史现算，收尾的任务提醒另加在末尾，不改写已保存的历史。

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
	_ = json.Unmarshal(row.Content, &message)
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
// 每条用户发来的消息后接当时的时间；连续每满 4 轮没有可见正文的工具调用后插一条进度提醒。
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
			// 时间取这条消息的入库时间，同一段历史每次组装仍完全相同。
			if row.Sender != nil && *row.Sender == "user" {
				messages = append(messages, runtimeMessage(currentTimeMessageName, fmt.Sprintf(currentTimePrompt, row.CreatedAt.UTC().Format(time.RFC3339))))
				silentToolRounds = 0
			}
		case "assistant":
			messages = append(messages, requestMessage(row))
			for _, call := range message.ToolCalls {
				messages = append(messages, results[call.ID])
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

// buildRequestMessages 注入固定指令后，直接展开数据库中的摘要与历史，不额外读取或注入记忆文件。
func buildRequestMessages(rows []database.ListAgentContextRowsRow) []json.RawMessage {
	return append([]json.RawMessage{runtimeMessage(instructionsMessageName, MainInstructions)}, assembleMessages(rows)...)
}

// buildAgentRequest 组装固定指令与历史，并附上全部工具定义打包成完整请求。
// taskReminder 为 true 时这一轮由收尾提醒触发，在历史末尾追加一条任务提醒请模型先核对任务状态。
func buildAgentRequest(rows []database.ListAgentContextRowsRow, model string, outputLimit int32, taskReminder bool) modelRequest {
	messages := buildRequestMessages(rows)
	// 任务提醒只存在于这一轮请求里，不落库、不改写已有历史，历史前缀保持逐字不变。
	if taskReminder {
		messages = append(messages, runtimeMessage(taskReminderMessageName, taskIncompleteReminder))
	}
	// 只取实际组装的这批行的最大序号；摘要排在首位，其物理序号可能大于保留的原文。
	var inputSequence int64
	for _, row := range rows {
		inputSequence = max(inputSequence, row.Sequence)
	}
	return modelRequest{InputSequence: inputSequence, Model: model, Messages: messages, Tools: tools.Definitions(), MaxTokens: outputLimit}
}

// buildCompactRequestMessages 复用普通请求的固定指令和历史前缀，包括已读取的工具结果，只在末尾追加压缩要求。
func buildCompactRequestMessages(rows []database.ListAgentContextRowsRow, summaryPrompt string) []json.RawMessage {
	return append(buildRequestMessages(rows), runtimeMessage("compaction", compactInstructions+"\n\n"+summaryPrompt))
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
		pending := []toolCall{}
		for _, call := range message.ToolCalls {
			if !answered[call.ID] {
				pending = append(pending, call)
			}
		}
		return pending, *rows[index].StepID
	}
	return nil, uuid.Nil
}
