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

// requestMessage 把一条已存消息转成请求中的原生消息：只删前端展示字段与任务的旧消息绑定字段，正文、工具调用与渠道原生字段原样保留。
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
	stripTaskMessageBindings(message)
	raw, _ := json.Marshal(message)
	return raw
}

// stripTaskMessageBindings 删掉历史里任务与消息的旧绑定：旧登记调用参数中的 source_message_id 与旧任务回执中的 related_message_ids。
// 数据库原文不动，只影响发给模型的请求，免得模型照旧范例继续传已删除的参数。
func stripTaskMessageBindings(message map[string]json.RawMessage) {
	// assistant 的工具调用参数是 JSON 字符串，旧登记调用在其中存了来源消息 ID。
	if rawCalls, ok := message["tool_calls"]; ok {
		var calls []map[string]json.RawMessage
		if json.Unmarshal(rawCalls, &calls) == nil {
			changed := false
			for _, call := range calls {
				var function map[string]json.RawMessage
				if json.Unmarshal(call["function"], &function) != nil {
					continue
				}
				var name string
				if json.Unmarshal(function["name"], &name) != nil || name != "record_task_input" {
					continue
				}
				var argumentsText string
				if json.Unmarshal(function["arguments"], &argumentsText) != nil {
					continue
				}
				var arguments map[string]json.RawMessage
				if json.Unmarshal([]byte(argumentsText), &arguments) != nil {
					continue
				}
				if _, ok := arguments["source_message_id"]; !ok {
					continue
				}
				delete(arguments, "source_message_id")
				rawArguments, err := json.Marshal(arguments)
				if err != nil {
					continue
				}
				function["arguments"], err = json.Marshal(string(rawArguments))
				if err != nil {
					continue
				}
				call["function"], err = json.Marshal(function)
				if err != nil {
					continue
				}
				changed = true
			}
			if changed {
				if rawCalls, err := json.Marshal(calls); err == nil {
					message["tool_calls"] = rawCalls
				}
			}
		}
	}
	// Gemini 同模型续接会原样回传 gemini_content 里的原生工具调用，其 args 里同样存了来源消息 ID。
	if rawGeminiContent, ok := message["gemini_content"]; ok {
		var geminiContent map[string]json.RawMessage
		if json.Unmarshal(rawGeminiContent, &geminiContent) == nil {
			var parts []map[string]json.RawMessage
			if json.Unmarshal(geminiContent["parts"], &parts) == nil {
				changed := false
				for _, part := range parts {
					var functionCall map[string]json.RawMessage
					if json.Unmarshal(part["functionCall"], &functionCall) != nil {
						continue
					}
					var name string
					if json.Unmarshal(functionCall["name"], &name) != nil || name != "record_task_input" {
						continue
					}
					var args map[string]json.RawMessage
					if json.Unmarshal(functionCall["args"], &args) != nil {
						continue
					}
					if _, ok := args["source_message_id"]; !ok {
						continue
					}
					delete(args, "source_message_id")
					rawArgs, err := json.Marshal(args)
					if err != nil {
						continue
					}
					functionCall["args"] = rawArgs
					rawCall, err := json.Marshal(functionCall)
					if err != nil {
						continue
					}
					part["functionCall"] = rawCall
					changed = true
				}
				if changed {
					if rawParts, err := json.Marshal(parts); err == nil {
						geminiContent["parts"] = rawParts
						if updated, err := json.Marshal(geminiContent); err == nil {
							message["gemini_content"] = updated
						}
					}
				}
			}
		}
	}
	// tool 回执的正文是工具返回的文本，旧任务回执在其中的任务记录里带相关消息列表。
	var role string
	if json.Unmarshal(message["role"], &role) != nil || role != "tool" {
		return
	}
	var content string
	if json.Unmarshal(message["content"], &content) != nil {
		return
	}
	if stripped, changed := stripRelatedMessageIDs(content); changed {
		if rawContent, err := json.Marshal(stripped); err == nil {
			message["content"] = rawContent
		}
	}
}

// stripRelatedMessageIDs 递归删除一段 JSON 文本中所有 related_message_ids 键，返回处理后的文本与是否删过。
func stripRelatedMessageIDs(text string) (string, bool) {
	var value any
	if json.Unmarshal([]byte(text), &value) != nil {
		return text, false
	}
	if !removeRelatedMessageIDs(value) {
		return text, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return text, false
	}
	return string(raw), true
}

// removeRelatedMessageIDs 在已解析的 JSON 值里递归删除 related_message_ids 键，报告是否删过。
func removeRelatedMessageIDs(value any) bool {
	changed := false
	switch value := value.(type) {
	case map[string]any:
		if _, ok := value["related_message_ids"]; ok {
			delete(value, "related_message_ids")
			changed = true
		}
		for _, child := range value {
			if removeRelatedMessageIDs(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range value {
			if removeRelatedMessageIDs(child) {
				changed = true
			}
		}
	}
	return changed
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

// buildRequestMessages 注入固定指令后，直接展开数据库中的摘要与历史，不额外读取或注入记忆文件。
func buildRequestMessages(rows []database.ListAgentContextRowsRow) []json.RawMessage {
	return append([]json.RawMessage{runtimeMessage(instructionsMessageName, MainInstructions)}, assembleMessages(rows)...)
}

// buildAgentRequest 组装固定指令与历史，再校验工具配对，并附上全部工具定义打包成完整请求。
// taskReminder 为 true 时这一轮由收尾提醒触发，在历史末尾追加一条任务提醒请模型先核对任务状态。
func buildAgentRequest(rows []database.ListAgentContextRowsRow, model string, outputLimit int32, taskReminder bool) (modelRequest, error) {
	messages := buildRequestMessages(rows)
	// 任务提醒只存在于这一轮请求里，不落库、不改写已有历史，历史前缀保持逐字不变。
	if taskReminder {
		messages = append(messages, runtimeMessage(taskReminderMessageName, taskIncompleteReminder))
	}
	if err := validateRequestMessages(messages); err != nil {
		return modelRequest{}, err
	}
	// 只取实际组装的这批行的最大序号；摘要排在首位，其物理序号可能大于保留的原文。
	var inputSequence int64
	for _, row := range rows {
		inputSequence = max(inputSequence, row.Sequence)
	}
	return modelRequest{InputSequence: inputSequence, Model: model, Messages: messages, Tools: tools.Definitions(), MaxTokens: outputLimit}, nil
}

// buildCompactRequestMessages 复用普通请求的固定指令和历史前缀，包括已读取的工具结果，只在末尾追加压缩要求。
func buildCompactRequestMessages(rows []database.ListAgentContextRowsRow, summaryPrompt string) []json.RawMessage {
	return append(buildRequestMessages(rows), runtimeMessage("compaction", compactInstructions+"\n\n"+summaryPrompt))
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
