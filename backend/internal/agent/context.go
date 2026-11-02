// 本文件负责把数据库消息组装成模型请求：第一条注入固定指令，其后是上一个 compact 以来的原生消息；发出前修复缺失的工具回执，并确认最后一条能由模型回应。

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// parsedContextMessage 是组装上下文时从一条已存消息里读出的共用字段。
type parsedContextMessage struct {
	Role       string          `json:"role"`
	Name       string          `json:"name"`
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

// loadContextRows 读入该用户 Main 当前世代的上下文行，供组装与压缩共用同一份顺序。
func loadContextRows(ctx context.Context, queries *database.Queries, userID uuid.UUID) ([]database.ListAgentContextRowsRow, error) {
	return queries.ListAgentContextRows(ctx, userID)
}

// instructionsMessage 是每次请求第一条注入的不可见 user 消息：固定指令不以 system 角色发送，也不落库，改动随代码部署对全部 Main 生效。
func instructionsMessage() json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"role": "user", "name": instructionsMessageName, "content": mainInstructions})
	return raw
}

// contextRequestMessage 把一条已存消息转成请求中的原生消息：旧落库指令行跳过（指令已由第一条注入替代），其余只删前端展示字段，正文、工具调用与渠道原生字段原样保留。
func contextRequestMessage(row database.ListAgentContextRowsRow) (json.RawMessage, bool) {
	if parseContextMessage(row.Content).Name == instructionsMessageName {
		return nil, false
	}
	var message map[string]json.RawMessage
	if json.Unmarshal(row.Content, &message) != nil {
		return row.Content, true
	}
	// 界面引用位置和文件名只用于历史展示，不发送给模型供应商。
	delete(message, "user_text")
	delete(message, "quotes")
	delete(message, "image_names")
	delete(message, "parts")
	raw, _ := json.Marshal(message)
	return raw, true
}

// buildRequestMessages 组装一次普通请求：第一条是指令注入，其后是当前世代每条消息的原生形态，最后修复缺失的工具回执。
func buildRequestMessages(rows []database.ListAgentContextRowsRow) []json.RawMessage {
	messages := []json.RawMessage{instructionsMessage()}
	for _, row := range rows {
		if raw, ok := contextRequestMessage(row); ok {
			messages = append(messages, raw)
		}
	}
	return repairMissingToolResults(messages)
}

// buildCompactRequestMessages 组装一次压缩请求：压缩指令作为不可见 user 消息在前，待压历史保持原生形态，末尾一条用户消息提出摘要要求。
func buildCompactRequestMessages(rows []database.ListAgentContextRowsRow, summaryPrompt string) []json.RawMessage {
	compactInstructionsMessage, _ := json.Marshal(map[string]string{"role": "user", "content": compactInstructions})
	messages := []json.RawMessage{compactInstructionsMessage}
	for _, row := range rows {
		if raw, ok := contextRequestMessage(row); ok {
			messages = append(messages, raw)
		}
	}
	messages = repairMissingToolResults(messages)
	summaryRequestMessage, _ := json.Marshal(map[string]string{"role": "user", "content": summaryPrompt})
	return append(messages, summaryRequestMessage)
}

// repairMissingToolResults 在内存里给缺回执的工具调用补一条中断回执，紧跟在对应的 assistant 之后，保证发出的请求满足供应商对 tool_call_id 配对的要求；落库历史不改，真正的恢复仍由运行循环按回执重跑或收尾补齐。
func repairMissingToolResults(messages []json.RawMessage) []json.RawMessage {
	repaired := make([]json.RawMessage, 0, len(messages))
	for index, raw := range messages {
		repaired = append(repaired, raw)
		message := parseContextMessage(raw)
		if message.Role != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		// 先收集紧随其后的已有回执，缺哪个调用就补哪个的中断回执。
		existing := map[string]bool{}
		for _, following := range messages[index+1:] {
			followingMessage := parseContextMessage(following)
			if followingMessage.Role != "tool" {
				break
			}
			existing[followingMessage.ToolCallID] = true
		}
		for _, call := range message.ToolCalls {
			if existing[call.ID] {
				continue
			}
			interrupted, _ := json.Marshal(map[string]string{"role": "tool", "tool_call_id": call.ID, "content": "Error: execution was interrupted before this tool call completed."})
			repaired = append(repaired, interrupted)
		}
	}
	return repaired
}

// validateRequestMessages 是发出请求前的最后一道检查：每个工具调用都有回执，且最后一条是 user 或 tool（有待模型回应的内容）；最后一条是已结束的 assistant 说明已经回复完毕，不应再发请求。
func validateRequestMessages(messages []json.RawMessage) error {
	if len(messages) == 0 {
		return fmt.Errorf("model request has no messages")
	}
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

// ensureCurrentTimeMessage 在领到新消息的步骤第一次准备请求时，把当前时间作为一条不可见 user 消息追加到尾部；同一步重试复用已存的那条。
func (r *Runner) ensureCurrentTimeMessage(ctx context.Context, connection *pgxpool.Conn, userID uuid.UUID, stepID uuid.UUID, rows []database.ListAgentContextRowsRow) ([]database.ListAgentContextRowsRow, error) {
	if stepID == uuid.Nil {
		return rows, nil
	}
	queries := database.New(connection)
	exists, err := queries.HasStepCurrentTime(ctx, &stepID)
	if err != nil || exists {
		return rows, err
	}
	content, _ := json.Marshal(map[string]string{"role": "user", "name": currentTimeMessageName, "content": fmt.Sprintf(currentTimePrompt, time.Now().UTC().Format(time.RFC3339))})
	sender := "system"
	err = pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, userID); err != nil {
			return err
		}
		return queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: userID, Role: "user", Content: content, StepID: &stepID, Sender: &sender})
	})
	if err != nil {
		return nil, err
	}
	return loadContextRows(ctx, queries, userID)
}

// rawContextMessages 只取出上下文行的原始 JSON，供进度提醒等仍按单条消息判断的逻辑使用。
func rawContextMessages(rows []database.ListAgentContextRowsRow) []json.RawMessage {
	messages := make([]json.RawMessage, len(rows))
	for index, row := range rows {
		messages[index] = row.Content
	}
	return messages
}
