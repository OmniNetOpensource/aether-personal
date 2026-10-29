// 本文件负责聊天历史的读取与转换：分页查询消息、转换成前端使用的消息结构，并计算已读位置。

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const historyPageSize = 50

type chatMessage struct {
	ID              string     `json:"id"`
	Position        uuid.UUID  `json:"position"`
	ClientMessageID *uuid.UUID `json:"client_message_id,omitempty"`
	Sender          string     `json:"sender"`
	Text            string     `json:"text"`
	CreatedAt       time.Time  `json:"created_at"`
}

type chatMessages struct {
	Messages []chatMessage `json:"messages"`
	ReadUpTo *uuid.UUID    `json:"read_up_to"`
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

	readUpTo, err := s.readUpTo(ctx, userID)
	if err != nil {
		return chatMessages{}, err
	}
	return chatMessages{Messages: chatMessagesFromRows(rowsNewestFirst), ReadUpTo: readUpTo}, nil
}

func chatMessagesFromRows(rows []database.ListChatRowsAfterRow) []chatMessage {
	messages := []chatMessage{}
	for _, row := range rows {
		messages = append(messages, chatMessagesFromRow(row)...)
	}
	return messages
}

func chatMessagesFromRow(row database.ListChatRowsAfterRow) []chatMessage {
	if row.Role != "assistant" {
		var content struct {
			Content string `json:"content"`
		}
		json.Unmarshal(row.Content, &content)
		return []chatMessage{{
			ID:              row.ID.String(),
			Position:        row.ID,
			ClientMessageID: row.ClientMessageID,
			Sender:          "user",
			Text:            content.Content,
			CreatedAt:       row.CreatedAt,
		}}
	}

	var messages []chatMessage
	for _, sent := range agent.MessagesToUser(row.Content) {
		messages = append(messages, chatMessage{
			ID:        sent.ToolCallID,
			Position:  row.ID,
			Sender:    "main",
			Text:      sent.Text,
			CreatedAt: row.CreatedAt,
		})
	}
	return messages
}

func (s *server) readUpTo(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error) {
	messageID, err := s.queries.GetReadUpTo(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &messageID, nil
}
