// 本文件管理聊天工作区文件的 OSS 流式读写与下载签名：文件保存在用户专属子目录，下载链接按原文件名返回。

package images

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/google/uuid"
)

// MaxFileBytes 是单个工作区附件与单条消息全部附件合计的字节上限（50 MiB）。
const MaxFileBytes = 50 << 20

// File 保存工作区文件在消息中的持久对象引用与展示信息；URL 只在读取聊天记录时临时签名。
type File struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	StorageURL string `json:"storage_url"`
	Size       int64  `json:"size"`
	MIMEType   string `json:"mime_type"`
}

// CleanFileName 提取安全的单层文件名，去掉路径分隔符与控制字符，供下载头与工作区落盘共用。
func CleanFileName(raw string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	base := strings.TrimSpace(filepath.Base(trimmed))
	var cleaned strings.Builder
	for _, r := range base {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			continue
		}
		cleaned.WriteRune(r)
	}
	result := strings.Trim(cleaned.String(), " ")
	if result == "" || result == "." || result == ".." {
		return "attachment.bin"
	}
	if utf8.RuneCountInString(result) > 200 {
		runes := []rune(result)
		ext := []rune(filepath.Ext(result))
		if len(ext) > 0 && len(ext) < 20 {
			result = string(runes[:200-len(ext)]) + string(ext)
		} else {
			result = string(runes[:200])
		}
	}
	return result
}

// cleanMIMEType 规范化客户端或系统上报的媒体类型，缺失或含非法字符时统一回退为二进制流。
func cleanMIMEType(raw string) string {
	mediaType, _, _ := strings.Cut(strings.TrimSpace(raw), ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" || len(mediaType) > 128 || !strings.Contains(mediaType, "/") || strings.ContainsAny(mediaType, " \t\r\n\"'\\") {
		return "application/octet-stream"
	}
	return mediaType
}

// countingReader 在流式上传期间统计实际写入的字节数，避免把大文件整块缓存在内存里。
type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (c *countingReader) Read(buffer []byte) (int, error) {
	readBytes, err := c.reader.Read(buffer)
	c.bytes += int64(readBytes)
	return readBytes, err
}

// PutFile 将流式读取的文件写入当前用户专属的 files 子目录：先读入首个字节确认非空，再通过调用方传入的限长流写入 OSS，无需 oss:DeleteObject 权限。
func (s *Store) PutFile(ctx context.Context, userID uuid.UUID, name, mimeType string, body io.Reader) (File, error) {
	var firstByte [1]byte
	readFirst, firstErr := io.ReadFull(body, firstByte[:])
	if readFirst == 0 {
		if firstErr == io.EOF || firstErr == io.ErrUnexpectedEOF {
			return File{}, fmt.Errorf("文件不能为空")
		}
		return File{}, firstErr
	}
	client, settings, err := s.client(ctx)
	if err != nil {
		return File{}, err
	}
	cleanName := CleanFileName(name)
	cleanMIME := cleanMIMEType(mimeType)
	key := "aether-chat/" + userID.String() + "/files/" + uuid.NewString()
	counter := &countingReader{reader: io.MultiReader(bytes.NewReader(firstByte[:]), body)}
	_, err = client.PutObject(ctx, &oss.PutObjectRequest{
		Bucket:          oss.Ptr(settings.Bucket),
		Key:             oss.Ptr(key),
		Body:            counter,
		ContentType:     oss.Ptr(cleanMIME),
		CacheControl:    oss.Ptr("private, max-age=86400"),
		ForbidOverwrite: oss.Ptr("true"),
	})
	if err != nil {
		return File{}, fmt.Errorf("OSS 文件上传失败: %w", err)
	}
	if counter.bytes > MaxFileBytes {
		return File{}, fmt.Errorf("单个文件不能超过 50 MB")
	}
	storageURL := "oss://" + settings.Bucket + "/" + key
	downloadURL, err := s.DownloadURL(ctx, storageURL, cleanName)
	if err != nil {
		return File{}, err
	}
	return File{Name: cleanName, URL: downloadURL, StorageURL: storageURL, Size: counter.bytes, MIMEType: cleanMIME}, nil
}

// OwnedFileReference 检查文件对象引用属于当前用户的 files 子目录，与视觉图片引用严格隔离。
func OwnedFileReference(reference string, userID uuid.UUID) bool {
	parsed, err := url.Parse(reference)
	if err != nil || parsed.Scheme != "oss" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	prefix := "/aether-chat/" + userID.String() + "/files/"
	return strings.HasPrefix(parsed.Path, prefix) && !strings.Contains(strings.TrimPrefix(parsed.Path, prefix), "/") && !strings.Contains(parsed.Path, "..")
}

// fileObjectKey 只接受当前配置 Bucket 下 aether-chat/*/files/* 的内部文件对象。
func fileObjectKey(reference, bucket string) (string, error) {
	parsed, err := url.Parse(reference)
	if err != nil || parsed.Scheme != "oss" || parsed.Host != bucket || !strings.HasPrefix(parsed.Path, "/aether-chat/") || !strings.Contains(parsed.Path, "/files/") || strings.Contains(parsed.Path, "..") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", fmt.Errorf("无效的 OSS 文件引用")
	}
	return strings.TrimPrefix(parsed.Path, "/"), nil
}

// DownloadURL 签发一天有效的私有文件下载链接，并通过 RFC 5987 Content-Disposition 还原原始中英文文件名。
func (s *Store) DownloadURL(ctx context.Context, reference, fileName string) (string, error) {
	client, settings, err := s.client(ctx)
	if err != nil {
		return "", err
	}
	key, err := fileObjectKey(reference, settings.Bucket)
	if err != nil {
		return "", err
	}
	cleanName := CleanFileName(fileName)
	disposition := "attachment; filename*=UTF-8''" + url.PathEscape(cleanName)
	result, err := client.Presign(ctx, &oss.GetObjectRequest{
		Bucket:                     oss.Ptr(settings.Bucket),
		Key:                        oss.Ptr(key),
		ResponseContentDisposition: oss.Ptr(disposition),
	}, oss.PresignExpires(24*time.Hour))
	if err != nil {
		return "", fmt.Errorf("OSS 文件下载链接生成失败")
	}
	return result.URL, nil
}

// InspectFile 向 OSS 读取文件真实大小与媒体类型，发消息入口据此校验总体积。
func (s *Store) InspectFile(ctx context.Context, reference string) (int64, string, error) {
	client, settings, err := s.client(ctx)
	if err != nil {
		return 0, "", err
	}
	key, err := fileObjectKey(reference, settings.Bucket)
	if err != nil {
		return 0, "", err
	}
	result, err := client.HeadObject(ctx, &oss.HeadObjectRequest{Bucket: oss.Ptr(settings.Bucket), Key: oss.Ptr(key)})
	if err != nil || result.ContentLength <= 0 || result.ContentLength > MaxFileBytes {
		return 0, "", fmt.Errorf("OSS 文件不存在或超出大小限制")
	}
	mimeType := "application/octet-stream"
	if result.ContentType != nil && *result.ContentType != "" {
		mimeType = cleanMIMEType(*result.ContentType)
	}
	return result.ContentLength, mimeType, nil
}

// OpenObject 打开当前 Bucket 下任意聊天图片或文件的读取流，供沙箱在启动时把附件同步进 /mnt/workspace/uploads。
func (s *Store) OpenObject(ctx context.Context, reference string) (io.ReadCloser, error) {
	client, settings, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	key, err := fileObjectKey(reference, settings.Bucket)
	if err != nil {
		key, err = objectKey(reference, settings.Bucket)
		if err != nil {
			return nil, err
		}
	}
	result, err := client.GetObject(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(settings.Bucket), Key: oss.Ptr(key)})
	if err != nil {
		return nil, fmt.Errorf("读取 OSS 附件失败: %w", err)
	}
	return result.Body, nil
}
