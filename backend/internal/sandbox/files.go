// 本文件负责聊天与沙箱 /mnt/workspace 的双向文件流转：按需把用户的聊天附件流式导入工作区，并把工作区产物发回聊天。

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	e2b "github.com/aliyun-fc/e2b-go-sdk"
	"github.com/google/uuid"
)

const workspaceRoot = "/mnt/workspace"

// cleanWorkspacePath 校验模型给出的工作区绝对路径，拒绝跳出 /mnt/workspace 的路径穿越。
func cleanWorkspacePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("path must be an absolute path inside /mnt/workspace")
	}
	cleaned := path.Clean(trimmed)
	if cleaned == workspaceRoot || !strings.HasPrefix(cleaned, workspaceRoot+"/") {
		return "", fmt.Errorf("path must point to a file inside /mnt/workspace")
	}
	return cleaned, nil
}

// ImportFile 将当前用户的聊天图片或文件从私有 OSS 流式写入 /mnt/workspace 下的指定路径，路径与归属校验先于实例唤醒。
func (s *Store) ImportFile(ctx context.Context, userID uuid.UUID, rawSource, rawPath string) (summary string, err error) {
	source := strings.TrimSpace(rawSource)
	if !images.OwnedFileReference(source, userID) && !images.OwnedReference(source, userID) {
		return "Error: source must be an oss:// attachment reference belonging to this user", nil
	}
	targetPath, err := cleanWorkspacePath(rawPath)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	instance, _, err := s.ensure(ctx, userID)
	if err != nil {
		return "", err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer stop()
		if timeoutErr := instance.SetTimeout(cleanup, int(s.config.Timeout.Seconds())); timeoutErr != nil {
			err = errors.Join(err, fmt.Errorf("update sandbox idle timeout after importing file: %w", timeoutErr))
		}
	}()
	// 若目标位于子目录，先按 mkdir -p 语义确保父目录存在，模型无需提前单独建目录。
	if parentDir := path.Dir(targetPath); parentDir != workspaceRoot {
		if _, err := instance.Files.MakeDir(ctx, parentDir, e2b.WithFileUser("user"), e2b.WithFileRequestTimeout(30*time.Second)); err != nil {
			return "", fmt.Errorf("create workspace directory %s: %w", parentDir, err)
		}
	}
	// 等沙箱就绪后再打开 OSS 读取流，直接将对象字节流式推入工作区，不在宿主机落临时文件或整块读入内存。
	stream, err := s.images.OpenObject(ctx, source)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	if _, err := instance.Files.Write(ctx, targetPath, stream,
		e2b.WithFileUser("user"),
		e2b.WithOctetStreamUpload(true),
		e2b.WithFileRequestTimeout(4*time.Minute),
		e2b.WithFileStreamIdleTimeout(60*time.Second),
	); err != nil {
		return "", fmt.Errorf("write %s to %s: %w", source, targetPath, err)
	}
	return fmt.Sprintf("Imported %s to %s.", source, targetPath), nil
}

// SendFile 从 /mnt/workspace 读取指定文件并存入私有 OSS：4 MiB 以内支持格式图片作为预览图发送，其余作为可下载文件发送。
// 返回给模型的文本摘要与写入聊天记录的可见 parts 一并返回。
func (s *Store) SendFile(ctx context.Context, userID uuid.UUID, rawPath string) (summary string, chatParts json.RawMessage, err error) {
	targetPath, err := cleanWorkspacePath(rawPath)
	if err != nil {
		return "Error: " + err.Error(), nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	instance, _, err := s.ensure(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer stop()
		if timeoutErr := instance.SetTimeout(cleanup, int(s.config.Timeout.Seconds())); timeoutErr != nil {
			err = errors.Join(err, fmt.Errorf("update sandbox idle timeout after sending file: %w", timeoutErr))
		}
	}()
	info, err := instance.Files.GetInfo(ctx, targetPath, e2b.WithFileUser("user"), e2b.WithFileRequestTimeout(30*time.Second))
	if err != nil {
		return fmt.Sprintf("Error: cannot inspect %s: %v", targetPath, err), nil, nil
	}
	// Unix S_IFDIR 位（0040000）表示目录，不能直接当作单个文件发送。
	if info.Mode&0o040000 != 0 {
		return fmt.Sprintf("Error: %s is a directory; archive it (for example as .zip) before sending", targetPath), nil, nil
	}
	if info.Size <= 0 {
		return fmt.Sprintf("Error: %s is empty", targetPath), nil, nil
	}
	if info.Size > images.MaxFileBytes {
		return fmt.Sprintf("Error: %s is %d bytes, exceeding the 50 MiB limit", targetPath, info.Size), nil, nil
	}

	fileName := images.CleanFileName(path.Base(targetPath))
	stream, err := instance.Files.ReadStream(ctx, targetPath,
		e2b.WithFileUser("user"),
		e2b.WithFileRequestTimeout(4*time.Minute),
		e2b.WithFileStreamIdleTimeout(60*time.Second),
	)
	if err != nil {
		return "", nil, fmt.Errorf("read %s from workspace: %w", targetPath, err)
	}
	defer stream.Close()

	// 4 MiB 以内先读入内存检查是否为受支持的聊天图片：是则直接发可预览图片气泡，否则回退为普通文件卡片。
	if info.Size <= images.MaxImageBytes {
		data, readErr := io.ReadAll(io.LimitReader(stream, images.MaxImageBytes+1))
		if readErr != nil {
			return "", nil, fmt.Errorf("read %s bytes: %w", targetPath, readErr)
		}
		if int64(len(data)) <= images.MaxImageBytes {
			if _, validErr := images.ValidateBytes(data); validErr == nil {
				storedImage, putErr := s.images.Put(ctx, userID, fileName, data)
				if putErr != nil {
					return "", nil, putErr
				}
				storedImage.URL = ""
				partsJSON, marshalErr := json.Marshal([]map[string]any{{"type": "image", "image": storedImage}})
				if marshalErr != nil {
					return "", nil, marshalErr
				}
				return fmt.Sprintf("Sent image %s (%d bytes) from %s to the user in chat.", storedImage.Name, storedImage.Size, targetPath), partsJSON, nil
			}
			detectedMIME := mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName)))
			if detectedMIME == "" {
				detectedMIME = http.DetectContentType(data)
			}
			storedFile, putErr := s.images.PutFile(ctx, userID, fileName, detectedMIME, strings.NewReader(string(data)))
			if putErr != nil {
				return "", nil, putErr
			}
			storedFile.URL = ""
			partsJSON, marshalErr := json.Marshal([]map[string]any{{"type": "file", "file": storedFile}})
			if marshalErr != nil {
				return "", nil, marshalErr
			}
			return fmt.Sprintf("Sent file %s (%d bytes) from %s to the user in chat.", storedFile.Name, storedFile.Size, targetPath), partsJSON, nil
		}
	}

	detectedMIME := mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName)))
	if detectedMIME == "" {
		detectedMIME = "application/octet-stream"
	}
	storedFile, err := s.images.PutFile(ctx, userID, fileName, detectedMIME, stream)
	if err != nil {
		return "", nil, err
	}
	storedFile.URL = ""
	partsJSON, err := json.Marshal([]map[string]any{{"type": "file", "file": storedFile}})
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Sent file %s (%d bytes) from %s to the user in chat.", storedFile.Name, storedFile.Size, targetPath), partsJSON, nil
}
