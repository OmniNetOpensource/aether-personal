// 本文件定义与具体供应商无关的模型协议接口与配置：调用方只需按供应商传入不同 Config，即可统一构造请求、解码流式响应并拉取模型目录。

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"
)

// Protocol 标识模型接口使用的底层协议族，不绑定任何具体供应商。
type Protocol string

const (
	ProtocolOpenAI          Protocol = "openai"
	ProtocolOpenAIResponses Protocol = "openai_responses"
	ProtocolGemini          Protocol = "gemini"
	ProtocolClaude          Protocol = "claude"
)

// ValidProtocol 检查协议名是否属于已实现的协议族。
func ValidProtocol(protocol Protocol) bool {
	switch protocol {
	case ProtocolOpenAI, ProtocolOpenAIResponses, ProtocolGemini, ProtocolClaude:
		return true
	default:
		return false
	}
}

// Config 是调用任意模型端点所需的连接与协议参数；不同供应商只是传入不同的 Config。
type Config struct {
	Protocol        Protocol
	BaseURL         string
	APIKey          string
	SendSessionID   bool
	EnableReasoning bool
}

// Request 是上层发给任意模型协议的统一请求：Model 为上游真实模型名，消息与工具保留原始 JSON，由各协议自行转换。
type Request struct {
	// InputSequence 是本次上下文快照的最大序号，运行时用它识别更新的输入。
	InputSequence int64 `json:"-"`
	// SessionID 标识当前 Agent 身份与上下文世代，仅在支持粘性路由的 OpenAI 兼容配置上序列化。
	SessionID     string            `json:"session_id,omitempty"`
	Reasoning     map[string]any    `json:"reasoning,omitempty"`
	Model         string            `json:"model"`
	Messages      []json.RawMessage `json:"messages"`
	Tools         []json.RawMessage `json:"tools,omitempty"`
	ToolChoice    json.RawMessage   `json:"tool_choice,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	StreamOptions map[string]bool   `json:"stream_options,omitempty"`
}

// Response 是任意协议交回上层的统一响应：正文、工具调用与用量都已整理成与协议无关的形状。
type Response struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     *int32           `json:"prompt_tokens"`
		CachedTokens     *int32           `json:"cached_tokens"`
		CompletionTokens *int32           `json:"completion_tokens"`
		Cost             *decimal.Decimal `json:"cost"`
	} `json:"usage"`
}

// OutgoingRequest 是按协议构造好的 HTTP 请求；PreventRedirect 防止自定义密钥头在重定向时泄露给其他主机。
type OutgoingRequest struct {
	Request         *http.Request
	Body            []byte
	PreventRedirect bool
}

// CatalogModel 是模型目录返回的单个上游模型项，ID 与 Name 均不含渠道前缀。
type CatalogModel struct {
	ID            string
	Name          string
	ContextLength *int64
}

// historyToolCall 是从历史消息中读出的共用工具调用形状，Gemini 与 Claude 转换工具历史时共用。
type historyToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// PrepareRequest 按 Config.Protocol 将统一请求转为对应协议的流式 HTTP 请求。
func PrepareRequest(ctx context.Context, config Config, request Request) (OutgoingRequest, error) {
	switch config.Protocol {
	case ProtocolOpenAI:
		return prepareOpenAIRequest(ctx, config, request)
	case ProtocolOpenAIResponses:
		return prepareOpenAIResponsesRequest(ctx, config, request)
	case ProtocolGemini:
		return prepareGeminiRequest(ctx, config, request)
	case ProtocolClaude:
		return prepareClaudeRequest(ctx, config, request)
	default:
		return OutgoingRequest{}, fmt.Errorf("unsupported model protocol: %s", config.Protocol)
	}
}

// DecodeStream 按协议将完整接收的 SSE 分片拼回统一响应；incomplete 为真表示流在完成前断开，可重试。
func DecodeStream(protocol Protocol, payloads [][]byte, request Request) (Response, bool, error) {
	switch protocol {
	case ProtocolOpenAI:
		return decodeOpenAIStream(payloads)
	case ProtocolOpenAIResponses:
		return decodeOpenAIResponsesStream(payloads, request)
	case ProtocolGemini:
		return decodeGeminiStream(payloads, request)
	case ProtocolClaude:
		return decodeClaudeStream(payloads, request)
	default:
		return Response{}, false, fmt.Errorf("unsupported model protocol: %s", protocol)
	}
}

// ListModels 按 Config.Protocol 读取目标端点的可用模型目录，不依赖具体供应商名称。
func ListModels(ctx context.Context, client *http.Client, config Config) ([]CatalogModel, error) {
	switch config.Protocol {
	case ProtocolOpenAI:
		return listOpenAIModels(ctx, client, config)
	case ProtocolOpenAIResponses:
		return listOpenAIResponsesModels(ctx, client, config)
	case ProtocolGemini:
		return listGeminiModels(ctx, client, config)
	case ProtocolClaude:
		return listClaudeModels(ctx, client, config)
	default:
		return nil, fmt.Errorf("unsupported model protocol: %s", config.Protocol)
	}
}

// noRedirectClient 复制 HTTP 客户端并禁止自动跟随重定向，避免把密钥头转发到其他主机。
func noRedirectClient(client *http.Client) http.Client {
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return copyClient
}
