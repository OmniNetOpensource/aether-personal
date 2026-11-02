// 本文件实现 read_message 工具：按 ID 读回当前用户一条消息的原文，配合 search_message 的命中结果使用。

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// readMessageTool 登记 read_message：读回原文与回执在同一事务里提交。
var readMessageTool = Tool{
	Name:       "read_message",
	Definition: json.RawMessage(`{"type":"function","function":{"name":"read_message","description":"Read the full text of one message returned by search_message, with its speaker and time.","parameters":{"type":"object","additionalProperties":false,"properties":{"message_id":{"type":"string","description":"A message UUID returned by search_message"}},"required":["message_id"]}}}`),
	Execute:    executeReadMessage,
}

// executeReadMessage 按 ID 读回一条消息的完整提取文本；不属于当前用户的 ID 与不存在的 ID 返回同一句错误，不泄露归属。
func executeReadMessage(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
	var parsed struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return "Error: arguments are not valid JSON", nil
	}
	messageID, err := uuid.Parse(parsed.MessageID)
	if err != nil {
		return "Error: message_id must be a message UUID returned by search_message", nil
	}
	row, err := queries.GetHistoryMessage(ctx, database.GetHistoryMessageParams{UserID: userID, MessageID: messageID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "Error: message not found in your searchable history", nil
	}
	if err != nil {
		return "", err
	}
	// 极长的消息（多为工具抓回的网页全文）截断返回，避免一次读回就占满上下文；截断会明确标出。
	text := row.SearchText
	truncated := false
	if runes := []rune(text); len(runes) > 32000 {
		text = string(runes[:32000])
		truncated = true
	}
	result := struct {
		MessageID uuid.UUID `json:"message_id"`
		Role      string    `json:"role"`
		Sender    *string   `json:"sender,omitempty"`
		IsSummary bool      `json:"is_summary"`
		CreatedAt time.Time `json:"created_at"`
		Text      string    `json:"text"`
		Truncated bool      `json:"truncated,omitempty"`
	}{MessageID: row.ID, Role: row.Role, Sender: row.Sender, IsSummary: row.IsSummary, CreatedAt: row.CreatedAt, Text: text, Truncated: truncated}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
