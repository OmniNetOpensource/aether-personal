// 本文件封装聊天图片的 OSS 存储：上传后填入共享 Redis 缓存，模型请求优先读缓存并转换为 base64，历史返回临时预览链接。

package images

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const MaxImageBytes = 4 << 20
const MaxMessageImageBytes = 8 << 20

// Image 的 StorageURL 是持久引用，URL 只用于短期预览，不写入数据库。
type Image struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	StorageURL string `json:"storage_url"`
	Size       int    `json:"size"`
	MIMEType   string `json:"mime_type"`
}

// Store 跨后端实例通过 Redis 共享图片原始字节缓存；服务凭证仍在每次操作时从看板配置读取。
type Store struct {
	services      *services.Store
	redisClient   *redis.Client
	cacheMaxBytes int64
	cacheTTL      time.Duration
}

// client 将服务配置转换成官方 SDK 客户端；连接和读取受调用方超时控制，兜底上限覆盖 50 MB 文件流传输。
func (s *Store) client(ctx context.Context) (*oss.Client, services.OSSSettings, error) {
	settings, err := s.services.LoadOSS(ctx)
	if err != nil {
		return nil, settings, err
	}
	cfg := oss.LoadDefaultConfig().WithRegion(settings.Region).WithCredentialsProvider(credentials.NewStaticCredentialsProvider(settings.AccessKeyID, settings.AccessKeySecret)).WithHttpClient(&http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	return oss.NewClient(cfg), settings, nil
}

// ValidateBytes 在图片上传和 OSS 读取这两个外部边界核对真实字节，不信任浏览器的 MIME 声明。
func ValidateBytes(data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return "", fmt.Errorf("图片必须非空且不超过 4 MB")
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return mime, nil
	}
	return "", fmt.Errorf("仅支持 PNG、JPEG、GIF 和 WebP 图片")
}

// Put 将图片写入当前用户专属前缀，随机对象名避免同名文件覆盖。
func (s *Store) Put(ctx context.Context, userID uuid.UUID, name string, data []byte) (Image, error) {
	mime, err := ValidateBytes(data)
	if err != nil {
		return Image{}, err
	}
	client, settings, err := s.client(ctx)
	if err != nil {
		return Image{}, err
	}
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/gif": "gif", "image/webp": "webp"}[mime]
	key := "aether-chat/" + userID.String() + "/" + uuid.NewString() + "." + ext
	_, err = client.PutObject(ctx, &oss.PutObjectRequest{Bucket: oss.Ptr(settings.Bucket), Key: oss.Ptr(key), Body: bytes.NewReader(data), ContentType: oss.Ptr(mime), CacheControl: oss.Ptr("private, max-age=86400"), ForbidOverwrite: oss.Ptr("true")})
	if err != nil {
		return Image{}, fmt.Errorf("OSS 图片上传失败")
	}
	image := Image{Name: name, StorageURL: "oss://" + settings.Bucket + "/" + key, Size: len(data), MIMEType: mime}
	// 上传成功后直接写入共享 Redis 缓存，无论下一跳落在哪个后端实例都不必从 OSS 重新下载。
	if err := s.writeCachedImage(ctx, image.StorageURL, data); err != nil {
		return Image{}, err
	}
	image.URL, err = s.Preview(ctx, image.StorageURL)
	return image, err
}

// OwnedReference 在用户消息的入口检查对象归属，禁止把其他用户的图片放入自己的上下文。
func OwnedReference(reference string, userID uuid.UUID) bool {
	parsed, err := url.Parse(reference)
	if err != nil || parsed.Scheme != "oss" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	prefix := "/aether-chat/" + userID.String() + "/"
	return strings.HasPrefix(parsed.Path, prefix) && !strings.Contains(strings.TrimPrefix(parsed.Path, prefix), "/") && !strings.Contains(parsed.Path, "..")
}

// objectKey 只接受当前配置 Bucket 的内部对象，不对用户输入的 HTTP URL 发起抓取。
func objectKey(reference, bucket string) (string, error) {
	parsed, err := url.Parse(reference)
	if err != nil || parsed.Scheme != "oss" || parsed.Host != bucket || !strings.HasPrefix(parsed.Path, "/aether-chat/") || strings.Contains(parsed.Path, "..") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", fmt.Errorf("无效的 OSS 图片引用")
	}
	return strings.TrimPrefix(parsed.Path, "/"), nil
}

// Preview 生成一天有效的私有对象链接；每次历史读取重新签名，签名不作为持久引用保存。
func (s *Store) Preview(ctx context.Context, reference string) (string, error) {
	client, settings, err := s.client(ctx)
	if err != nil {
		return "", err
	}
	key, err := objectKey(reference, settings.Bucket)
	if err != nil {
		return "", err
	}
	result, err := client.Presign(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(settings.Bucket), Key: oss.Ptr(key)}, oss.PresignExpires(24*time.Hour))
	if err != nil {
		return "", fmt.Errorf("OSS 图片链接生成失败")
	}
	return result.URL, nil
}

// ResolveMessages 在渠道适配之前替换 OSS 引用，OpenRouter 和 Gemini 因而拿到相同的 base64 内容。
func (s *Store) ResolveMessages(ctx context.Context, messages []json.RawMessage) ([]json.RawMessage, error) {
	resolved := make([]json.RawMessage, len(messages))
	cache := map[string]string{}
	for index, raw := range messages {
		resolved[index] = raw
		if !bytes.Contains(raw, []byte("oss://")) {
			continue
		}
		var message map[string]json.RawMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, err
		}
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil {
			continue
		}
		for _, block := range blocks {
			var kind string
			_ = json.Unmarshal(block["type"], &kind)
			if kind != "image_url" {
				continue
			}
			var image struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(block["image_url"], &image)
			if !strings.HasPrefix(image.URL, "oss://") {
				continue
			}
			encoded, exists := cache[image.URL]
			if !exists {
				client, settings, err := s.client(ctx)
				if err != nil {
					return nil, err
				}
				key, err := objectKey(image.URL, settings.Bucket)
				if err != nil {
					return nil, err
				}
				data, err := s.readCachedImage(ctx, image.URL)
				if err != nil {
					return nil, err
				}
				// 只有未命中或过期才下载，校验成功后写入共享缓存；其他错误直接交给请求后阶段收尾。
				if data == nil {
					result, err := client.GetObject(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(settings.Bucket), Key: oss.Ptr(key)})
					if err != nil {
						return nil, fmt.Errorf("OSS 图片读取失败（获取 %s）: %w", image.URL, err)
					}
					data, err = io.ReadAll(io.LimitReader(result.Body, MaxImageBytes+1))
					if err := errors.Join(err, result.Body.Close()); err != nil {
						return nil, fmt.Errorf("OSS 图片读取失败（读取 %s）: %w", image.URL, err)
					}
					if _, err := ValidateBytes(data); err != nil {
						return nil, err
					}
					if err := s.writeCachedImage(ctx, image.URL, data); err != nil {
						return nil, err
					}
				}
				mime := http.DetectContentType(data)
				encoded = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
				cache[image.URL] = encoded
			}
			block["image_url"], _ = json.Marshal(map[string]string{"url": encoded})
		}
		message["content"], _ = json.Marshal(blocks)
		resolved[index], _ = json.Marshal(message)
	}
	return resolved, nil
}

// Inspect 优先从共享 Redis 缓存核对真实字节大小与类型，未命中时再向 OSS 读取对象元信息，不信任客户端回传的 size。
func (s *Store) Inspect(ctx context.Context, reference string) (int, string, error) {
	client, settings, err := s.client(ctx)
	if err != nil {
		return 0, "", err
	}
	key, err := objectKey(reference, settings.Bucket)
	if err != nil {
		return 0, "", err
	}
	cachedData, err := s.readCachedImage(ctx, reference)
	if err != nil {
		return 0, "", err
	}
	if cachedData != nil {
		mime, err := ValidateBytes(cachedData)
		if err != nil {
			return 0, "", err
		}
		return len(cachedData), mime, nil
	}
	result, err := client.HeadObject(ctx, &oss.HeadObjectRequest{Bucket: oss.Ptr(settings.Bucket), Key: oss.Ptr(key)})
	if err != nil || result.ContentLength <= 0 || result.ContentLength > MaxImageBytes || result.ContentType == nil {
		return 0, "", fmt.Errorf("OSS 图片不存在或超出大小限制")
	}
	return int(result.ContentLength), *result.ContentType, nil
}
