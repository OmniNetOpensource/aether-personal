// 本文件定义模型渠道的共用契约与自动选择：Agent 只给出统一请求，按模型名从注册表选出渠道，渠道差异都留在各自文件里。

package providers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/shopspring/decimal"
)

// Request 是 Agent 发给任意渠道的统一请求：消息与工具保留原始 JSON，各渠道在自己的文件里翻成自家协议。
type Request struct {
	// InputSequence 是本次上下文快照的最大序号，运行时用它识别更新的输入。
	InputSequence int64 `json:"-"`
	// RequireOutput 表示用户还没收到正文，不能把无正文、无工具调用的响应当成完成。
	RequireOutput bool              `json:"-"`
	Reasoning     map[string]any    `json:"reasoning,omitempty"`
	Model         string            `json:"model"`
	Messages      []json.RawMessage `json:"messages"`
	Tools         []json.RawMessage `json:"tools,omitempty"`
	ToolChoice    json.RawMessage   `json:"tool_choice,omitempty"`
	MaxTokens     int32             `json:"max_tokens,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	StreamOptions map[string]bool   `json:"stream_options,omitempty"`
}

// Response 是任意渠道交回 Agent 的统一响应：正文、工具调用与用量都已拼成与渠道无关的形状。
type Response struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     *int32           `json:"prompt_tokens"`
		CompletionTokens *int32           `json:"completion_tokens"`
		Cost             *decimal.Decimal `json:"cost"`
	} `json:"usage"`
}

// OutgoingRequest 是渠道已构造好的 HTTP 请求：Body 单独返回，供调用方写入模型调用日志。
type OutgoingRequest struct {
	Request         *http.Request
	Body            []byte
	PreventRedirect bool
}

// Provider 是一个模型渠道的协议适配：认领自己负责的模型，把统一请求构造为自家 HTTP 请求，再把流式分片拼回统一响应。
type Provider interface {
	Matches(model string) bool
	Service() string
	PrepareRequest(ctx context.Context, credentials services.Settings, request Request) (OutgoingRequest, error)
	HasOutput(payload []byte) bool
	DecodeStream(payloads [][]byte, request Request) (response Response, incomplete bool, err error)
}

// registry 按顺序认领模型：先问有前缀约定的渠道，最后由 OpenRouter 兜底承接其余全部模型；
// 新增渠道只需实现 Provider 并在此登记一行，Agent 的请求流程不变。
var registry = []Provider{geminiProvider{}, openRouterProvider{}}

// ForModel 按模型名自动选择渠道，OpenRouter 的 Matches 恒为真，所以永远有渠道可用。
func ForModel(model string) Provider {
	for _, provider := range registry {
		if provider.Matches(model) {
			return provider
		}
	}
	return openRouterProvider{}
}
