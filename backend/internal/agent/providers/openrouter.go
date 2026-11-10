// 本文件适配 OpenRouter 渠道：设置 Gemini 的默认推理强度，组装流式请求，并保留响应中的正文、推理与工具调用供保存和续接。

package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/shopspring/decimal"
)

// openRouterProvider 承接除已认领的 ikun 模型之外的全部模型，是注册表里的兜底渠道。
type openRouterProvider struct{}

// Matches 恒为真：注册表按顺序询问，轮到这里的模型都归 OpenRouter。
func (openRouterProvider) Matches(string) bool { return true }

// Service 返回看板中保存 OpenRouter 凭证的服务名。
func (openRouterProvider) Service() string { return services.OpenRouter }

// PrepareRequest 把统一请求序列化为 OpenRouter 的流式聊天请求，并剥掉只属于其他渠道的原生协议字段。
func (openRouterProvider) PrepareRequest(ctx context.Context, credentials services.Settings, prepared Request) (OutgoingRequest, error) {
	// 要求流式返回，并在末尾分片带上用量；用量拿不到时记空，不阻断回复。
	prepared.Stream = true
	prepared.StreamOptions = map[string]bool{"include_usage": true}
	// 普通 Gemini 请求明确使用高推理并返回思考；压缩已经指定低强度与隐藏推理，沿用其设置。
	if strings.HasPrefix(prepared.Model, "google/gemini-") && prepared.Reasoning == nil {
		prepared.Reasoning = map[string]any{"effort": "high", "exclude": false}
	}
	// 从 ikun 原生渠道切回 OpenRouter 时不向其他供应商泄漏原生协议元数据。
	messages := make([]json.RawMessage, len(prepared.Messages))
	copy(messages, prepared.Messages)
	for index, raw := range messages {
		if !bytes.Contains(raw, []byte(`"gemini_content"`)) && !bytes.Contains(raw, []byte(`"claude_content"`)) {
			continue
		}
		var message map[string]json.RawMessage
		_ = json.Unmarshal(raw, &message)
		delete(message, "gemini_content")
		delete(message, "gemini_model")
		delete(message, "claude_content")
		delete(message, "claude_model")
		messages[index], _ = json.Marshal(message)
	}
	prepared.Messages = messages
	body, err := json.Marshal(prepared)
	if err != nil {
		return OutgoingRequest{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return OutgoingRequest{}, err
	}
	request.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	request.Header.Set("Content-Type", "application/json")
	return OutgoingRequest{Request: request, Body: body}, nil
}

// HasOutput 在收到首段正文或工具调用时返回 true，忽略 reasoning、空正文与仅含用量的事件。
func (openRouterProvider) HasOutput(payload []byte) bool {
	var chunk openRouterStreamChunk
	if json.Unmarshal(payload, &chunk) != nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if strings.TrimSpace(streamContentText(choice.Delta.Content)) != "" || len(choice.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// DecodeStream 把 OpenRouter 的流式分片拼成一条完整响应。
func (openRouterProvider) DecodeStream(payloads [][]byte, _ Request) (Response, bool, error) {
	return assembleOpenRouterStream(payloads)
}

// openRouterStreamChunk 接收正文、推理和工具调用的流式分片，用量通常只在末尾分片出现。
type openRouterStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          json.RawMessage   `json:"content"`
			Reasoning        string            `json:"reasoning"`
			ReasoningDetails []json.RawMessage `json:"reasoning_details"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int32           `json:"prompt_tokens"`
		CompletionTokens *int32           `json:"completion_tokens"`
		Cost             *decimal.Decimal `json:"cost"`
	} `json:"usage"`
}

// streamToolCall 是按序号累积中的一条工具调用：名称与 ID 只在首个分片出现，参数靠后续分片逐段拼接。
type streamToolCall struct {
	id        string
	name      string
	arguments strings.Builder
}

// assembleOpenRouterStream 把 OpenRouter 的流式分片拼成与非流式相同的完整响应。
// 没收到 [DONE] 或结束原因说明流被截断，按可重试失败处理；分片本身不是合法 JSON 时按无效响应处理。
func assembleOpenRouterStream(payloads [][]byte) (Response, bool, error) {
	var response Response
	var content strings.Builder
	var reasoning strings.Builder
	var reasoningDetails []json.RawMessage
	calls := map[int]*streamToolCall{}
	finishReason := ""
	sawDone := false
	for _, payload := range payloads {
		if string(payload) == "[DONE]" {
			sawDone = true
			continue
		}
		var chunk openRouterStreamChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return response, false, fmt.Errorf("decode model stream chunk: %w", err)
		}
		if chunk.ID != "" {
			response.ID = chunk.ID
		}
		if chunk.Model != "" {
			response.Model = chunk.Model
		}
		if chunk.Usage != nil {
			response.Usage.PromptTokens = chunk.Usage.PromptTokens
			response.Usage.CompletionTokens = chunk.Usage.CompletionTokens
			response.Usage.Cost = chunk.Usage.Cost
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			content.WriteString(streamContentText(choice.Delta.Content))
			// 推理文本按流顺序拼接；详情分片保留原始顺序与字段，避免丢失加密签名或把同 index 的不同类型合并。
			reasoning.WriteString(choice.Delta.Reasoning)
			reasoningDetails = append(reasoningDetails, choice.Delta.ReasoningDetails...)
			for _, delta := range choice.Delta.ToolCalls {
				call := calls[delta.Index]
				if call == nil {
					call = &streamToolCall{}
					calls[delta.Index] = call
				}
				if delta.ID != "" {
					call.id = delta.ID
				}
				if delta.Function.Name != "" {
					call.name = delta.Function.Name
				}
				call.arguments.WriteString(delta.Function.Arguments)
			}
		}
	}
	if !sawDone || finishReason == "" {
		return response, true, fmt.Errorf("model stream ended before completion")
	}
	// 工具调用按模型给出的序号排序，再组装成一条与非流式响应同形状的助手消息。
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	toolCalls := []map[string]any{}
	for _, index := range indexes {
		call := calls[index]
		if call.name == "" {
			return response, false, fmt.Errorf("model stream returned a tool call without a name")
		}
		toolCalls = append(toolCalls, map[string]any{
			"id": call.id, "type": "function",
			"function": map[string]string{"name": call.name, "arguments": call.arguments.String()},
		})
	}
	// 推理与正文一起落进助手消息；下一轮原样回传，已有看板可直接读取文本或加密状态。
	messageFields := map[string]any{"role": "assistant", "content": content.String(), "tool_calls": toolCalls}
	if reasoning.Len() > 0 {
		messageFields["reasoning"] = reasoning.String()
	}
	if len(reasoningDetails) > 0 {
		messageFields["reasoning_details"] = reasoningDetails
	}
	message, err := json.Marshal(messageFields)
	if err != nil {
		return response, false, err
	}
	response.Choices = append(response.Choices, struct {
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	}{message, finishReason})
	return response, false, nil
}

// streamContentText 提取一个分片的正文增量：通常是字符串，少数供应商用分段数组时拼接其中的文本段。
func streamContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var combined strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			combined.WriteString(block.Text)
		}
	}
	return combined.String()
}
