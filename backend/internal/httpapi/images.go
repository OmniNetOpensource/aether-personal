// 本文件提供已登录用户的图片上传入口：读取原始图片字节、写入 OSS，并返回预览链接与持久引用。

package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
)

// uploadImage 只接收带 Bearer 令牌的图片请求，不使用 Cookie，跨站无法借用登录态上传。
func (s *server) uploadImage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || token == "" {
		http.Error(w, "请先登录", http.StatusUnauthorized)
		return
	}
	session, err := s.findSession(ctx, token)
	if err != nil {
		http.Error(w, "登录已过期，请重新登录", http.StatusUnauthorized)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, images.MaxImageBytes))
	if err != nil {
		http.Error(w, "图片不能超过 4 MB", http.StatusBadRequest)
		return
	}
	if _, err := images.ValidateBytes(data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name := r.URL.Query().Get("name")
	if len(name) > 1024 {
		http.Error(w, "图片文件名过长", http.StatusBadRequest)
		return
	}
	image, err := (images.Store{Services: s.services}).Put(ctx, session.UserID, name, data)
	if err != nil {
		if errors.Is(err, services.ErrNotConfigured) {
			http.Error(w, "请先在管理看板配置阿里云 OSS", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "图片上传失败，请检查 OSS 配置后重试", http.StatusBadGateway)
		return
	}
	writeAdminJSON(w, image)
}

// chatMessagesWithPreviews 为历史与实时推送签发 OSS 预览链接，签名不回写 messages。
func (s *server) chatMessagesWithPreviews(ctx context.Context, rows []database.ListChatRowsAfterRow) []chatMessage {
	messages := chatMessagesFromRows(rows)
	store := images.Store{Services: s.services}
	for index := range messages {
		for partIndex, part := range messages[index].Parts {
			if part.Image == nil {
				continue
			}
			image := *part.Image
			preview, err := store.Preview(ctx, image.StorageURL)
			if err == nil {
				image.URL = preview
			} else {
				image.URL = ""
			}
			messages[index].Parts[partIndex].Image = &image
		}
	}
	return messages
}

// checkMessageImages 在写入前按 OSS 真实大小核对整条消息，网络请求发生在数据库事务之外。
func (s *server) checkMessageImages(ctx context.Context, frame *clientFrame) bool {
	total := 0
	store := images.Store{Services: s.services}
	for _, part := range frame.Parts {
		if part.Image == nil {
			continue
		}
		size, mime, err := store.Inspect(ctx, part.Image.StorageURL)
		if err != nil || size != part.Image.Size || mime != part.Image.MIMEType {
			return false
		}
		total += size
	}
	return total <= images.MaxMessageImageBytes
}
