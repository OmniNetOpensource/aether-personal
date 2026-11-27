// 本文件实现对话消息的公开分享 HTTP 接口：生成不可猜测的分享链接、列出或取消当前用户的分享，并提供免登录的公开只读访问接口。

package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// maxShareMessages 限制单次打包分享的消息条数上限，避免超大请求拖慢签名与渲染。
const maxShareMessages = 200

// messageShareSummary 是返回给分享创建者的记录摘要，供生成回执与「已分享列表」共用。
type messageShareSummary struct {
	ID           uuid.UUID `json:"id"`
	Token        string    `json:"token"`
	CreatedAt    time.Time `json:"created_at"`
	MessageCount int32     `json:"message_count"`
	Preview      string    `json:"preview"`
}

// publicMessageShare 是访客打开分享链接时读取到的公开载荷，不包含分享者的邮箱或会话标识。
type publicMessageShare struct {
	Token     string        `json:"token"`
	CreatedAt time.Time     `json:"created_at"`
	Messages  []chatMessage `json:"messages"`
}

// validShareToken 核对公开 URL 中的分享令牌格式（22 至 64 位 URL 安全 Base64 字符）。
func validShareToken(token string) bool {
	if len(token) < 22 || len(token) > 64 {
		return false
	}
	for i := 0; i < len(token); i++ {
		ch := token[i]
		isAlphaNum := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		if !isAlphaNum && ch != '-' && ch != '_' {
			return false
		}
	}
	return true
}

// shareMessagePreview 提取首条分享消息的单行短摘录，与数据库列表查询的预览口径保持一致。
func shareMessagePreview(message chatMessage) string {
	var builder strings.Builder
	if len(message.Parts) > 0 {
		for _, part := range message.Parts {
			switch part.Type {
			case "text":
				builder.WriteString(part.Text)
				builder.WriteByte(' ')
			case "quote":
				if part.Quote != nil {
					builder.WriteString(part.Quote.Text)
					builder.WriteByte(' ')
				}
			case "image":
				if part.Image != nil {
					builder.WriteString(part.Image.Name)
					builder.WriteByte(' ')
				}
			case "file":
				if part.File != nil {
					builder.WriteString(part.File.Name)
					builder.WriteByte(' ')
				}
			}
		}
	} else {
		builder.WriteString(message.Text)
	}
	normalized := strings.Join(strings.Fields(builder.String()), " ")
	if utf8.RuneCountInString(normalized) <= 120 {
		return normalized
	}
	return string([]rune(normalized)[:120])
}

// listShares 处理 GET /shares：返回当前用户创建的全部公开分享摘要。
func (s *server) listShares(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	shares, err := s.listUserMessageShares(r.Context(), userID)
	if err != nil {
		internalError(w, "list message shares", err)
		return
	}
	writeAdminJSON(w, struct {
		Shares []messageShareSummary `json:"shares"`
	}{Shares: shares})
}

// createShare 处理 POST /shares：将当前用户选中的若干可见消息打包成一条公开只读分享记录。
func (s *server) createShare(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	var body struct {
		MessageIDs []uuid.UUID `json:"message_ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		http.Error(w, "请求格式无效", http.StatusBadRequest)
		return
	}
	summary, ok, err := s.createMessageShare(r.Context(), userID, body.MessageIDs)
	if err != nil {
		internalError(w, "create message share", err)
		return
	}
	if !ok {
		http.Error(w, "请选择有效且不超过 200 条的消息", http.StatusBadRequest)
		return
	}
	writeAdminJSON(w, struct {
		Share messageShareSummary `json:"share"`
	}{Share: summary})
}

// deleteShare 处理 DELETE /shares/{id}：撤销当前用户的指定分享记录。
func (s *server) deleteShare(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	shareID, err := uuid.Parse(r.PathValue("id"))
	if err != nil || shareID == uuid.Nil {
		http.Error(w, "该分享不存在或已被取消", http.StatusNotFound)
		return
	}
	deleted, err := s.queries.DeleteUserMessageShare(r.Context(), database.DeleteUserMessageShareParams{
		ShareID: shareID,
		UserID:  userID,
	})
	if err != nil {
		internalError(w, "delete message share", err)
		return
	}
	if deleted == 0 {
		http.Error(w, "该分享不存在或已被取消", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createMessageShare 在单个数据库事务中校验所选消息全部属于当前用户且可展示，再生成 128 位随机分享令牌并落库。
func (s *server) createMessageShare(ctx context.Context, userID uuid.UUID, rawMessageIDs []uuid.UUID) (messageShareSummary, bool, error) {
	seen := make(map[uuid.UUID]struct{}, len(rawMessageIDs))
	uniqueIDs := make([]uuid.UUID, 0, len(rawMessageIDs))
	for _, id := range rawMessageIDs {
		if id == uuid.Nil {
			return messageShareSummary{}, false, nil
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			uniqueIDs = append(uniqueIDs, id)
		}
	}
	if len(uniqueIDs) == 0 || len(uniqueIDs) > maxShareMessages {
		return messageShareSummary{}, false, nil
	}

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return messageShareSummary{}, false, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)

	var summary messageShareSummary
	valid := true
	err := pgx.BeginFunc(ctx, s.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		rows, err := queries.ListOwnedShareableMessages(ctx, database.ListOwnedShareableMessagesParams{
			UserID:     userID,
			MessageIds: uniqueIDs,
		})
		if err != nil {
			return err
		}
		if len(rows) != len(uniqueIDs) {
			valid = false
			return nil
		}

		orderedIDs := make([]uuid.UUID, 0, len(rows))
		var firstMessage chatMessage
		for index, row := range rows {
			rendered := chatMessagesFromRow(database.ListChatRowsAfterRow(row))
			if len(rendered) == 0 {
				valid = false
				return nil
			}
			if index == 0 {
				firstMessage = rendered[0]
			}
			orderedIDs = append(orderedIDs, row.ID)
		}

		share, err := queries.CreateMessageShare(ctx, database.CreateMessageShareParams{
			UserID: userID,
			Token:  token,
		})
		if err != nil {
			return err
		}
		if err := queries.InsertMessageShareItems(ctx, database.InsertMessageShareItemsParams{
			ShareID:    share.ID,
			MessageIds: orderedIDs,
		}); err != nil {
			return err
		}

		summary = messageShareSummary{
			ID:           share.ID,
			Token:        share.Token,
			CreatedAt:    share.CreatedAt,
			MessageCount: int32(len(orderedIDs)),
			Preview:      shareMessagePreview(firstMessage),
		}
		return nil
	})
	if err != nil {
		return messageShareSummary{}, false, err
	}
	return summary, valid, nil
}

// listUserMessageShares 查询当前用户已创建的全部公开分享记录（按时间倒序）。
func (s *server) listUserMessageShares(ctx context.Context, userID uuid.UUID) ([]messageShareSummary, error) {
	rows, err := s.queries.ListUserMessageShares(ctx, userID)
	if err != nil {
		return nil, err
	}
	shares := make([]messageShareSummary, 0, len(rows))
	for _, row := range rows {
		shares = append(shares, messageShareSummary{
			ID:           row.ID,
			Token:        row.Token,
			CreatedAt:    row.CreatedAt,
			MessageCount: row.MessageCount,
			Preview:      row.Preview,
		})
	}
	return shares, nil
}

// getPublicShare 提供免登录只读访问：按分享令牌读取关联消息并现场签发 24 小时有效的 OSS 附件链接。
func (s *server) getPublicShare(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := r.PathValue("token")
	if !validShareToken(token) {
		http.Error(w, "该分享不存在或已被取消", http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	share, err := s.queries.GetMessageShareByToken(ctx, token)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "该分享不存在或已被取消", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("get public message share failed", "error", err)
		http.Error(w, "服务暂时不可用，请稍后重试", http.StatusInternalServerError)
		return
	}

	sharedRows, err := s.queries.ListSharedMessageRows(ctx, share.ID)
	if err != nil {
		slog.Error("list shared messages failed", "share_id", share.ID, "error", err)
		http.Error(w, "服务暂时不可用，请稍后重试", http.StatusInternalServerError)
		return
	}

	chatRows := make([]database.ListChatRowsAfterRow, len(sharedRows))
	for index, row := range sharedRows {
		chatRows[index] = database.ListChatRowsAfterRow(row)
	}
	messages := s.chatMessagesWithPreviews(ctx, chatRows)
	if len(messages) == 0 {
		http.Error(w, "该分享不存在或已被取消", http.StatusNotFound)
		return
	}
	for index := range messages {
		messages[index].ClientMessageID = nil
	}

	writeAdminJSON(w, publicMessageShare{
		Token:     share.Token,
		CreatedAt: share.CreatedAt,
		Messages:  messages,
	})
}
