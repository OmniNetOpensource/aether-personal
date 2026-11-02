// 本文件是联网工具共用的 HTTP 客户端：统一发送 JSON 请求、限制响应大小、屏蔽重定向，并提供严格的参数解码供各联网工具复用。

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
)

// maxWebResponseBytes 是单个联网供应商响应的大小上限，防止异常响应撑爆内存。
const maxWebResponseBytes = 8 << 20

// webClient 是联网工具共用的执行端：持有 HTTP 客户端与服务凭证仓库，供应商请求都经它发出。
type webClient struct {
	httpClient *http.Client
	services   *services.Store
}

// newWebClient 从 Runner 注入的依赖组装联网客户端。
func newWebClient(deps Dependencies) webClient {
	return webClient{httpClient: deps.HTTPClient, services: deps.Services}
}

// postJSON 向联网供应商发送一次 JSON 请求并解码响应：非 2xx 与供应商自带错误对象都收成带主机名与状态码的错误。
func (c *webClient) postJSON(ctx context.Context, endpoint, header, credential string, body, result any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(header, credential)
	response, err := c.noRedirectClient().Do(request)
	if err != nil {
		return fmt.Errorf("web provider request failed: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxWebResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxWebResponseBytes {
		return errors.New("web provider response exceeds 8 MiB")
	}
	var failure struct {
		Error *struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
			RefID     string `json:"ref_id"`
		} `json:"error"`
	}
	json.Unmarshal(data, &failure)
	if response.StatusCode < 200 || response.StatusCode >= 300 || failure.Error != nil {
		details := ""
		if failure.Error != nil {
			details = boundedWebError(strings.Join([]string{failure.Error.Code, failure.Error.Message, failure.Error.RequestID, failure.Error.RefID}, " "))
		}
		return fmt.Errorf("%s HTTP %d: %s", request.URL.Hostname(), response.StatusCode, details)
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("%s returned invalid JSON: %w", request.URL.Hostname(), err)
	}
	return nil
}

// platformRedirect 只取一次重定向目标而不跟随，用于把短链与分享链接交给调用方逐跳校验。
func (c *webClient) platformRedirect(ctx context.Context, page *url.URL) (*url.URL, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, page.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AetherBot/1.0)")
	response, err := c.noRedirectClient().Do(request)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != 301 && response.StatusCode != 302 && response.StatusCode != 303 && response.StatusCode != 307 && response.StatusCode != 308 {
		return nil, fmt.Errorf("share URL returned HTTP %d without a redirect", response.StatusCode)
	}
	location := response.Header.Get("Location")
	if location == "" {
		return nil, errors.New("share URL redirect is missing a location")
	}
	next, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	resolved := page.ResolveReference(next)
	resolved.Host = strings.ToLower(resolved.Host)
	return resolved, nil
}

// noRedirectClient 复制一份不跟随重定向的客户端，避免凭证头被转发给其他主机，重定向由调用方显式逐跳处理。
func (c *webClient) noRedirectClient() *http.Client {
	client := *c.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

// decodeArguments 严格解码工具参数：必须是单个 JSON 对象且不含未知字段，供应商参数拼错时尽早报错。
func decodeArguments(args json.RawMessage, result any) error {
	args = bytes.TrimSpace(args)
	if len(args) == 0 || args[0] != '{' {
		return errors.New("tool arguments must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("tool arguments must contain one JSON object")
	}
	return nil
}

// boundedWebError 把供应商错误文本截到 500 字节以内，避免错误回执过长占满上下文。
func boundedWebError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
