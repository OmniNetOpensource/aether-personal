// 本文件负责聊天历史的读取与转换：分页查询消息并转换成前端使用的消息结构。

package httpapi

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
)

const historyPageSize = 50

type chatMessage struct {
	ID              string        `json:"id"`
	Position        uuid.UUID     `json:"position"`
	ClientMessageID *uuid.UUID    `json:"client_message_id,omitempty"`
	Sender          string        `json:"sender"`
	Text            string        `json:"text"`
	Parts           []messagePart `json:"parts,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
}

type chatMessages struct {
	Messages []chatMessage `json:"messages"`
}

// historyPage 从 before 往前取一页用户可见消息，供 ready、authenticated 与 history 事件共用。
func (s *server) historyPage(ctx context.Context, userID uuid.UUID, before uuid.UUID) (chatMessages, error) {
	// 部分行（Main 未发消息给用户的 assistant 行）不产生消息，因此按行翻页直到凑满一页或没有更早的行。
	var rowsNewestFirst []database.ListChatRowsAfterRow
	messageCount := 0
	for messageCount < historyPageSize {
		rows, err := s.queries.ListChatRowsBefore(ctx, database.ListChatRowsBeforeParams{UserID: userID, ID: before})
		if err != nil {
			return chatMessages{}, err
		}
		for _, row := range rows {
			rowsNewestFirst = append(rowsNewestFirst, database.ListChatRowsAfterRow(row))
			messageCount += len(chatMessagesFromRow(database.ListChatRowsAfterRow(row)))
		}
		if len(rows) < historyPageSize {
			break
		}
		before = rows[len(rows)-1].ID
	}
	slices.Reverse(rowsNewestFirst)
	return chatMessages{Messages: s.chatMessagesWithPreviews(ctx, rowsNewestFirst)}, nil
}

func chatMessagesFromRows(rows []database.ListChatRowsAfterRow) []chatMessage {
	messages := []chatMessage{}
	for _, row := range rows {
		messages = append(messages, chatMessagesFromRow(row)...)
	}
	return messages
}

func chatMessagesFromRow(row database.ListChatRowsAfterRow) []chatMessage {
	if row.Role == "tool" {
		var content struct {
			Parts []messagePart `json:"parts"`
		}
		_ = json.Unmarshal(row.Content, &content)
		if len(content.Parts) == 0 {
			return nil
		}
		var textBuilder strings.Builder
		for _, part := range content.Parts {
			if part.Type == "text" {
				textBuilder.WriteString(part.Text)
			}
		}
		return []chatMessage{{
			ID:        row.ID.String(),
			Position:  row.ID,
			Sender:    "main",
			Text:      strings.TrimSpace(textBuilder.String()),
			Parts:     content.Parts,
			CreatedAt: row.CreatedAt,
		}}
	}
	if row.Role != "assistant" {
		var content struct {
			Content  json.RawMessage `json:"content"`
			UserText *string         `json:"user_text"`
			Parts    []messagePart   `json:"parts"`
		}
		json.Unmarshal(row.Content, &content)
		var text string
		if content.UserText != nil {
			text = *content.UserText
		} else {
			json.Unmarshal(content.Content, &text)
		}

		return []chatMessage{{
			ID:              row.ID.String(),
			Position:        row.ID,
			ClientMessageID: row.ClientMessageID,
			Sender:          "user",
			Text:            text,
			Parts:           content.Parts,
			CreatedAt:       row.CreatedAt,
		}}
	}

	// Main 的一条助手消息至多产生一条用户消息，正文就是消息内容，显示 ID 与所在行共用行 ID。
	text := agent.AssistantTextToUser(row.Content)
	if text == "" {
		return nil
	}
	return []chatMessage{{
		ID:        row.ID.String(),
		Position:  row.ID,
		Sender:    "main",
		Text:      text,
		CreatedAt: row.CreatedAt,
	}}
}
