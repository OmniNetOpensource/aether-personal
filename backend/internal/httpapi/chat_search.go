// 本文件负责页面历史搜索与上下文读取：只返回当前用户可见的聊天，与 Agent 内部历史检索分开。

package httpapi

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
)

// chatSearchHit 只携带结果列表需要的片段，完整消息由上下文请求按需读取。
type chatSearchHit struct {
	ID        uuid.UUID `json:"id"`
	Sender    string    `json:"sender"`
	CreatedAt time.Time `json:"created_at"`
	Excerpt   string    `json:"excerpt"`
}

// searchChatMessages 转义 LIKE 元字符，使百分号、下划线和反斜线也按用户输入的原文匹配。
func (s *server) searchChatMessages(ctx context.Context, userID uuid.UUID, keyword string, before uuid.UUID) (searchResultsEvent, error) {
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(keyword) + "%"
	rows, err := s.queries.SearchChatMessages(ctx, database.SearchChatMessagesParams{UserID: userID, BeforeID: before, Keyword: keyword, Pattern: pattern})
	if err != nil {
		return searchResultsEvent{}, err
	}
	result := searchResultsEvent{Type: "search_results", Results: []chatSearchHit{}}
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
func (s *server) messageContext(ctx context.Context, userID, messageID uuid.UUID, direction string) (messageContextEvent, error) {
	anchor, err := s.queries.GetChatMessage(ctx, database.GetChatMessageParams{UserID: userID, MessageID: messageID})
	if err != nil {
		return messageContextEvent{}, err
	}
	result := messageContextEvent{Type: "message_context"}
	var rows []database.ListChatRowsAfterRow
	// 前侧查询从近到远取，去掉用于探路的第 21 行后反转为聊天显示顺序。
	if direction != "newer" {
		before, err := s.queries.ListChatContextBefore(ctx, database.ListChatContextBeforeParams{UserID: userID, MessageID: messageID})
		if err != nil {
			return messageContextEvent{}, err
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
			return messageContextEvent{}, err
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
