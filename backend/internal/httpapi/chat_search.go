// 本文件负责页面历史搜索与上下文读取 HTTP 接口：只返回当前用户可见的聊天，与 Agent 内部历史检索分开。

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// chatSearchHit 只携带结果列表需要的片段，完整消息由上下文接口按需读取。
type chatSearchHit struct {
	ID        uuid.UUID `json:"id"`
	Sender    string    `json:"sender"`
	CreatedAt time.Time `json:"created_at"`
	Excerpt   string    `json:"excerpt"`
}

// chatSearchResponse 返回一页搜索命中结果及下一页游标，不改变正常聊天的历史断点。
type chatSearchResponse struct {
	Results    []chatSearchHit `json:"results"`
	NextBefore *uuid.UUID      `json:"next_before,omitempty"`
}

// messageContextResponse 提供独立历史窗口；两侧是否还有记录由多取的一行判断。
type messageContextResponse struct {
	HasMoreBefore bool `json:"has_more_before"`
	HasMoreAfter  bool `json:"has_more_after"`
	chatMessages
}

// searchMessagesHandler 处理 GET /messages/search：按关键词与可选游标搜索当前用户的可见消息。
func (s *server) searchMessagesHandler(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	keyword := strings.TrimSpace(r.URL.Query().Get("q"))
	if keyword == "" || utf8.RuneCountInString(keyword) > 200 {
		http.Error(w, "请输入 1 至 200 个字符的搜索词", http.StatusBadRequest)
		return
	}
	before := uuid.Max
	if rawBefore := strings.TrimSpace(r.URL.Query().Get("before")); rawBefore != "" {
		parsed, err := uuid.Parse(rawBefore)
		if err != nil {
			http.Error(w, "搜索分页游标无效", http.StatusBadRequest)
			return
		}
		before = parsed
	}
	result, err := s.searchChatMessages(r.Context(), userID, keyword, before)
	if err != nil {
		internalError(w, "search chat messages", err)
		return
	}
	writeAdminJSON(w, result)
}

// messageContextHandler 处理 GET /messages/{id}/context：按锚点与方向读取独立的历史上下文窗口。
func (s *server) messageContextHandler(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	messageID, err := uuid.Parse(r.PathValue("id"))
	if err != nil || messageID == uuid.Nil {
		http.Error(w, "消息标识无效", http.StatusBadRequest)
		return
	}
	direction := r.URL.Query().Get("direction")
	if direction != "around" && direction != "older" && direction != "newer" {
		http.Error(w, "上下文方向参数无效", http.StatusBadRequest)
		return
	}
	result, err := s.messageContext(r.Context(), userID, messageID, direction)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "找不到这条历史消息", http.StatusNotFound)
		return
	}
	if err != nil {
		internalError(w, "load chat message context", err)
		return
	}
	writeAdminJSON(w, result)
}

// searchChatMessages 转义 LIKE 元字符，使百分号、下划线和反斜线也按用户输入的原文匹配。
func (s *server) searchChatMessages(ctx context.Context, userID uuid.UUID, keyword string, before uuid.UUID) (chatSearchResponse, error) {
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword) + "%"
	rows, err := s.queries.SearchChatMessages(ctx, database.SearchChatMessagesParams{UserID: userID, BeforeID: before, Keyword: keyword, Pattern: pattern})
	if err != nil {
		return chatSearchResponse{}, err
	}
	result := chatSearchResponse{Results: []chatSearchHit{}}
	if len(rows) > 30 {
		rows = rows[:30]
		result.NextBefore = &rows[len(rows)-1].ID
	}
	for _, row := range rows {
		sender := "main"
		if row.Role == "user" {
			sender = "user"
		}
		result.Results = append(result.Results, chatSearchHit{ID: row.ID, Sender: sender, CreatedAt: row.CreatedAt, Excerpt: row.Excerpt})
	}
	return result, nil
}

// messageContext 读取锚点两侧或继续向一个方向翻页，不改变正常聊天连接的推送或历史断点。
func (s *server) messageContext(ctx context.Context, userID, messageID uuid.UUID, direction string) (messageContextResponse, error) {
	anchor, err := s.queries.GetChatMessage(ctx, database.GetChatMessageParams{UserID: userID, MessageID: messageID})
	if err != nil {
		return messageContextResponse{}, err
	}
	result := messageContextResponse{}
	var rows []database.ListChatRowsAfterRow
	// 前侧查询从近到远取，去掉用于探路的第 21 行后反转为聊天显示顺序。
	if direction != "newer" {
		before, err := s.queries.ListChatContextBefore(ctx, database.ListChatContextBeforeParams{UserID: userID, MessageID: messageID})
		if err != nil {
			return messageContextResponse{}, err
		}
		result.HasMoreBefore = len(before) > 20
		if result.HasMoreBefore {
			before = before[:20]
		}
		slices.Reverse(before)
		for _, row := range before {
			rows = append(rows, database.ListChatRowsAfterRow(row))
		}
	}
	if direction == "around" {
		rows = append(rows, database.ListChatRowsAfterRow(anchor))
	}
	// 后侧本身就是从旧到新排列；单向翻页不重复带回作为边界的锚点。
	if direction != "older" {
		after, err := s.queries.ListChatContextAfter(ctx, database.ListChatContextAfterParams{UserID: userID, MessageID: messageID})
		if err != nil {
			return messageContextResponse{}, err
		}
		result.HasMoreAfter = len(after) > 20
		if result.HasMoreAfter {
			after = after[:20]
		}
		for _, row := range after {
			rows = append(rows, database.ListChatRowsAfterRow(row))
		}
	}
	result.Messages = s.chatMessagesWithPreviews(ctx, rows)
	return result, nil
}
