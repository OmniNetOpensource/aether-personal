// 本文件把 AIO MCP 工具回执转换成模型内容段；截图存入用户的 OSS，消息历史只保存对象引用。

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// authenticatedTransport 为 FC 代理与 AIO 分别携带令牌，不把凭证写入 URL 或工具回执。
type authenticatedTransport struct {
	token   string
	traffic string
}

// RoundTrip 复制请求后加鉴权，保持 MCP SDK 原请求不变。
func (t authenticatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+t.token)
	request.Header.Set("e2b-traffic-access-token", t.traffic)
	return http.DefaultTransport.RoundTrip(request)
}

// ConnectMCP 创建一次短生命周期会话；重试交给模型，避免重复执行有副作用的桌面操作。
func ConnectMCP(ctx context.Context, endpoint, token, traffic string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "aether-personal", Version: "1"}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint,
		HTTPClient: &http.Client{Timeout: 120 * time.Second, Transport: authenticatedTransport{token: token, traffic: traffic}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		MaxRetries: -1, DisableStandaloneSSE: true}, nil)
}

// Call 执行登记过的工具；每个实例的工作目录和浏览器配置都位于 /mnt/workspace。
func (s *Store) Call(ctx context.Context, userID uuid.UUID, name, arguments string) (output string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	instance, token, err := s.ensure(ctx, userID)
	if err != nil {
		return "", err
	}
	// 即使 MCP 调用超时，也把实例交回 FC 的空闲暂停策略；不取消已经落在 NAS 的文件。
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer stop()
		if timeoutErr := instance.SetTimeout(cleanup, int(s.config.Timeout.Seconds())); timeoutErr != nil {
			err = errors.Join(err, fmt.Errorf("update sandbox idle timeout after tool execution: %w", timeoutErr))
		}
	}()
	session, err := ConnectMCP(ctx, "https://"+instance.GetHost(18091)+"/mcp", token, instance.TrafficAccessToken())
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, session.Close()) }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: json.RawMessage(arguments)})
	if err != nil {
		return "", err
	}
	parts := make([]any, 0, len(result.Content)+1)
	if result.IsError {
		parts = append(parts, map[string]any{"type": "text", "text": "Error: sandbox tool reported failure."})
	}
	for _, content := range result.Content {
		switch item := content.(type) {
		case *mcp.TextContent:
			parts = append(parts, map[string]any{"type": "text", "text": item.Text})
		case *mcp.ImageContent:
			image, err := s.images.Put(ctx, userID, "sandbox-screenshot", item.Data)
			if err != nil {
				return "", fmt.Errorf("save sandbox screenshot: %w", err)
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image.StorageURL}})
		default:
			return "", fmt.Errorf("unsupported sandbox MCP content %T", content)
		}
	}
	encoded, err := json.Marshal(parts)
	return string(encoded), err
}
