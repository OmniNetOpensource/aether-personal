// 本文件负责聊天消息的 HTTP 读写与模型转换：提供首屏/历史翻页、消息发送落库并唤醒 Main，以及用户可见消息的格式化。

package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// initialMessagesResponse 用于首屏加载：一次返回最新一页历史、实时推送游标及 Main 运行状态。
type initialMessagesResponse struct {
	Messages     []chatMessage `json:"messages"`
	ChangeCursor int64         `json:"change_cursor,string"`
	MainThinking bool          `json:"main_thinking"`
	MainFailed   bool          `json:"main_failed"`
}

// createMessageResponse 在消息落库后立即返还带预览签名的消息与变更游标，避免前端等待 WS 回推。
type createMessageResponse struct {
	Message      chatMessage `json:"message"`
	ChangeCursor int64       `json:"change_cursor,string"`
}

// listMessages 处理 GET /messages：带 before 时只翻一页更早历史，不带时返回首屏历史、变更游标与 Main 运行态。
func (s *server) listMessages(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	ctx := r.Context()
	if rawBefore := strings.TrimSpace(r.URL.Query().Get("before")); rawBefore != "" {
		before, err := uuid.Parse(rawBefore)
		if err != nil {
			http.Error(w, "历史分页游标无效", http.StatusBadRequest)
			return
		}
		page, err := s.historyPage(ctx, userID, before)
		if err != nil {
			internalError(w, "load message history page", err)
			return
		}
		writeAdminJSON(w, page)
		return
	}

	// 首屏先读取推送游标再取历史，期间并发落库的新消息在 WS 续推时由前端按 ID 幂等去重，不会遗漏。
	changeCursor, err := s.queries.GetLatestChatChange(ctx, userID)
	if err != nil {
		internalError(w, "load latest chat change cursor", err)
		return
	}
	page, err := s.historyPage(ctx, userID, uuid.Max)
	if err != nil {
		internalError(w, "load initial message history", err)
		return
	}
	thinking, failed, err := s.loadMainStatus(ctx, userID)
	if err != nil {
		internalError(w, "load main status", err)
		return
	}
	writeAdminJSON(w, initialMessagesResponse{
		Messages:     page.Messages,
		ChangeCursor: changeCursor,
		MainThinking: thinking,
		MainFailed:   failed,
	})
}

// createMessage 处理 POST /messages：校验输入与附件、在用户写入锁内落库并唤醒 Main，同时跨实例通知同账号其他连接。
func (s *server) createMessage(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	ctx := r.Context()
	var input sendMessageRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10)).Decode(&input); err != nil {
		http.Error(w, "消息格式无效或超出大小限制", http.StatusBadRequest)
		return
	}
	input, valid := normalizeMessageInput(input)
	if !valid || input.ClientMessageID == uuid.Nil || !validMessageInput(input, userID) || !s.checkMessageAttachments(ctx, userID, &input) {
		http.Error(w, "消息内容或附件无效", http.StatusBadRequest)
		return
	}

	var saved database.InsertUserMessageRow
	err := pgx.BeginFunc(ctx, s.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, userID); err != nil {
			return err
		}
		var err error
		saved, err = queries.InsertUserMessage(ctx, database.InsertUserMessageParams{
			UserID:          userID,
			Content:         userMessageContent(input),
			ClientMessageID: &input.ClientMessageID,
		})
		return err
	})
	if err != nil {
		internalError(w, "save user message", err)
		return
	}

	// 唤醒 Main 与跨端推送广播均使用脱离请求取消的短超时上下文，防止客户端提前断连截断通知。
	s.agents.Wake(ctx, userID)
	notifyCtx, cancelNotify := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	if err := agent.NotifyChat(notifyCtx, s.redisClient, userID); err != nil {
		slog.Error("publish user message chat notify failed", "user_id", userID, "error", err)
	}
	cancelNotify()

	rendered := s.chatMessagesWithPreviews(ctx, []database.ListChatRowsAfterRow{database.ListChatRowsAfterRow(saved)})
	writeAdminJSON(w, createMessageResponse{
		Message:      rendered[0],
		ChangeCursor: saved.ChangeCursor,
	})
}

// loadMainStatus 综合数据库待办、Redis 执行租约与后台 Subagent 状态，供首屏接口与 WS 推送共用。
func (s *server) loadMainStatus(ctx context.Context, userID uuid.UUID) (thinking bool, failed bool, err error) {
	status, err := s.queries.GetMainStatus(ctx, userID)
	if err != nil {
		return false, false, err
	}
	// 最终回复落库后的静默热压缩仍持有 Main 租约，此时无待办不亮气泡；压缩中途收到新输入后 main_work 变真立刻亮起。
	thinking = status.Thinking && s.agents != nil && s.agents.HasActiveLease(ctx, userID)
	if !thinking {
		subagentsRunning, err := s.queries.HasRunningSubagents(ctx, userID)
		if err != nil {
			return false, false, err
		}
		thinking = subagentsRunning
	}
	return thinking, status.Failed, nil
}

// historyPage 从 before 往前取一页用户可见消息，供首屏加载与向上翻页共用。
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
