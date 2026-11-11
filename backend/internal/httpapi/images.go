// 本文件提供已登录用户的图片与工作区文件入口：流式上传到 OSS、为历史消息签发预览或下载链接，并在入库前校验附件真实大小。

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
	"github.com/google/uuid"
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

// uploadFile 接收已绑定工作区用户的任意文件（上限 50 MiB），流式写入私有 OSS 供后续同步到 /mnt/workspace/uploads。
func (s *server) uploadFile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
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
	available, err := s.sandbox.Available(ctx, session.UserID)
	if err != nil {
		slog.Error("check workspace before file upload failed", "user_id", session.UserID, "error", err)
		http.Error(w, "服务暂时不可用，请稍后重试", http.StatusInternalServerError)
		return
	}
	if !available {
		http.Error(w, "当前账号尚未配置长期电脑工作区，仅支持发送 4 MB 以内的图片", http.StatusForbidden)
		return
	}
	name := r.URL.Query().Get("name")
	if len(name) > 1024 {
		http.Error(w, "文件名过长", http.StatusBadRequest)
		return
	}
	limited := http.MaxBytesReader(w, r.Body, images.MaxFileBytes)
	file, err := s.images.PutFile(ctx, session.UserID, name, r.Header.Get("Content-Type"), limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "单个文件不能超过 50 MB", http.StatusBadRequest)
			return
		}
		if errors.Is(err, services.ErrNotConfigured) {
			http.Error(w, "请先在管理看板配置阿里云 OSS", http.StatusServiceUnavailable)
			return
		}
		if strings.Contains(err.Error(), "不能为空") || strings.Contains(err.Error(), "不能超过") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		slog.Error("upload file failed", "user_id", session.UserID, "error", err)
		http.Error(w, "文件上传失败，请稍后重试", http.StatusBadGateway)
		return
	}
	writeAdminJSON(w, file)
}

// chatMessagesWithPreviews 为历史与实时推送本地签发 OSS 图片预览链接与文件下载链接，签名不回写 messages。
func (s *server) chatMessagesWithPreviews(ctx context.Context, rows []database.ListChatRowsAfterRow) []chatMessage {
	messages := chatMessagesFromRows(rows)
	for index := range messages {
		for partIndex, part := range messages[index].Parts {
			if part.Image != nil {
				image := *part.Image
				preview, err := s.images.Preview(ctx, image.StorageURL)
				if err == nil {
					image.URL = preview
				} else {
					image.URL = ""
				}
				messages[index].Parts[partIndex].Image = &image
			}
			if part.File != nil {
				file := *part.File
				download, err := s.images.DownloadURL(ctx, file.StorageURL, file.Name)
				if err == nil {
					file.URL = download
				} else {
					file.URL = ""
				}
				messages[index].Parts[partIndex].File = &file
			}
		}
	}
	return messages
}

// checkMessageAttachments 在写入前并行核对图片与工作区文件的真实大小，网络请求发生在数据库事务之外。
func (s *server) checkMessageAttachments(ctx context.Context, userID uuid.UUID, frame *clientFrame) bool {
	if len(frame.Files) > 0 {
		available, err := s.sandbox.Available(ctx, userID)
		if err != nil || !available {
			return false
		}
	}
	sizes := make([]int64, len(frame.Parts))
	imageSizes := make([]int, len(frame.Parts))
	group, checkCtx := errgroup.WithContext(ctx)
	for index, part := range frame.Parts {
		switch {
		case part.Image != nil:
			image := *part.Image
			group.Go(func() error {
				size, mime, err := s.images.Inspect(checkCtx, image.StorageURL)
				if err != nil || size != image.Size || mime != image.MIMEType {
					return errors.New("invalid message image")
				}
				imageSizes[index] = size
				sizes[index] = int64(size)
				return nil
			})
		case part.File != nil:
			file := *part.File
			group.Go(func() error {
				size, _, err := s.images.InspectFile(checkCtx, file.StorageURL)
				if err != nil || size != file.Size {
					return errors.New("invalid message file")
				}
				sizes[index] = size
				return nil
			})
		}
	}
	if group.Wait() != nil {
		return false
	}
	imageTotal := 0
	var total int64
	for index, size := range sizes {
		imageTotal += imageSizes[index]
		total += size
	}
	return imageTotal <= images.MaxMessageImageBytes && total <= images.MaxFileBytes
}
