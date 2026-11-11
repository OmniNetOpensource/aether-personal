// 本文件提供已登录用户的图片入口：通过共享存储上传到 OSS 并填充缓存，为历史签发预览链接并检查新消息的图片。

package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"golang.org/x/sync/errgroup"
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
	image, err := s.images.Put(ctx, session.UserID, name, data)
	if err != nil {
		if errors.Is(err, services.ErrNotConfigured) {
			http.Error(w, "请先在管理看板配置阿里云 OSS", http.StatusServiceUnavailable)
			return
		}
		slog.Error("upload image failed", "user_id", session.UserID, "error", err)
		http.Error(w, "图片上传失败，请稍后重试", http.StatusBadGateway)
		return
	}
	writeAdminJSON(w, image)
}

// chatMessagesWithPreviews 为历史与实时推送签发 OSS 预览链接，签名不回写 messages。
func (s *server) chatMessagesWithPreviews(ctx context.Context, rows []database.ListChatRowsAfterRow) []chatMessage {
	messages := chatMessagesFromRows(rows)
	for index := range messages {
		for partIndex, part := range messages[index].Parts {
			if part.Image == nil {
				continue
			}
			image := *part.Image
			preview, err := s.images.Preview(ctx, image.StorageURL)
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

// checkMessageImages 在写入前并行核对每张图片的真实大小与类型，网络请求发生在数据库事务之外。
func (s *server) checkMessageImages(ctx context.Context, frame *clientFrame) bool {
	sizes := make([]int, len(frame.Parts))
	group, checkCtx := errgroup.WithContext(ctx)
	for index, part := range frame.Parts {
		if part.Image == nil {
			continue
		}
		image := *part.Image
		group.Go(func() error {
			size, mime, err := s.images.Inspect(checkCtx, image.StorageURL)
			if err != nil || size != image.Size || mime != image.MIMEType {
				return errors.New("invalid message image")
			}
			sizes[index] = size
			return nil
		})
	}
	if group.Wait() != nil {
		return false
	}
	total := 0
	for _, size := range sizes {
		total += size
	}
	return total <= images.MaxMessageImageBytes
}
