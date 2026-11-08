// 本文件通过 ikun 的 Claude 原生 Messages 接口生成压缩摘要，转换历史文本、图片和工具轨迹，并记录完整流式用量。

package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
)

// claudeProvider 仅用于上下文压缩；看板不会把这些模型提供给 Main。
type claudeProvider struct{}

// Matches 认领带 ikun 渠道前缀的 Claude 模型。
func (claudeProvider) Matches(model string) bool { return strings.HasPrefix(model, "ikun:claude-") }

// Service 使用独立 Claude 凭证，不复用 Gemini 的令牌。
func (claudeProvider) Service() string { return services.IkunClaude }

// claudeMessage 保留消息顺序，工具回执按 Claude 协议放入 user 的 tool_result 块。
type claudeMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

// PrepareRequest 只变换协议，不改写历史正文；工具轨迹作为摘要材料，禁止生成新的工具调用。
func (claudeProvider) PrepareRequest(ctx context.Context, credentials services.Settings, prepared Request) (OutgoingRequest, error) {
	messages := []claudeMessage{}
	for _, raw := range prepared.Messages {
		var message struct {
			Role       string            `json:"role"`
			Content    json.RawMessage   `json:"content"`
			ToolCalls  []historyToolCall `json:"tool_calls"`
			ToolCallID string            `json:"tool_call_id"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return OutgoingRequest{}, fmt.Errorf("decode message for Claude: %w", err)
		}
		content := claudeMessage{Role: message.Role, Content: []json.RawMessage{}}
		// 共用内容只有文本和已解析为 base64 的图片，不向供应商传 Gemini 原生签名。
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			if text != "" {
				block, _ := json.Marshal(map[string]string{"type": "text", "text": text})
				content.Content = append(content.Content, block)
			}
		} else if len(message.Content) > 0 && message.Content[0] == '[' {
			var blocks []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			}
			if err := json.Unmarshal(message.Content, &blocks); err != nil {
				return OutgoingRequest{}, err
			}
			for _, block := range blocks {
				var encoded []byte
				switch block.Type {
				case "text":
					encoded, _ = json.Marshal(map[string]string{"type": "text", "text": block.Text})
				case "image_url":
					mime, data, ok := strings.Cut(strings.TrimPrefix(block.ImageURL.URL, "data:"), ";base64,")
					if !ok || !strings.HasPrefix(mime, "image/") {
						return OutgoingRequest{}, fmt.Errorf("Claude history requires base64 image data URLs")
					}
					encoded, _ = json.Marshal(map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": mime, "data": data}})
				default:
					return OutgoingRequest{}, fmt.Errorf("unsupported Claude history content: %s", block.Type)
				}
				content.Content = append(content.Content, encoded)
			}
		}
		switch message.Role {
		case "tool":
			block, _ := json.Marshal(map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": content.Content})
			content.Role, content.Content = "user", []json.RawMessage{block}
		case "assistant":
			for _, call := range message.ToolCalls {
				block, err := json.Marshal(map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": json.RawMessage(call.Function.Arguments)})
				if err != nil {
					return OutgoingRequest{}, fmt.Errorf("encode Claude tool history: %w", err)
				}
				content.Content = append(content.Content, block)
			}
		case "user":
		default:
			return OutgoingRequest{}, fmt.Errorf("unsupported Claude history role: %s", message.Role)
		}
		if len(content.Content) == 0 {
			continue
		}
		// 并行工具回执合为同一 user turn，确保它们全部紧跟发起调用的 assistant。
		if message.Role == "tool" && len(messages) > 0 && messages[len(messages)-1].Role == "user" {
			messages[len(messages)-1].Content = append(messages[len(messages)-1].Content, content.Content...)
		} else {
			messages = append(messages, content)
		}
	}
	// Claude 要求显式 max_tokens，使用模型目录的最大输出能力，不为摘要另设小额度。
	if prepared.MaxTokens <= 0 {
		return OutgoingRequest{}, fmt.Errorf("Claude model maximum output tokens are not configured")
	}
	tools := []json.RawMessage{}
	for _, raw := range prepared.Tools {
		var tool struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(raw, &tool); err != nil {
			return OutgoingRequest{}, err
		}
		encoded, err := json.Marshal(map[string]any{"name": tool.Function.Name, "description": tool.Function.Description, "input_schema": tool.Function.Parameters})
		if err != nil {
			return OutgoingRequest{}, err
		}
		tools = append(tools, encoded)
	}
	body, err := json.Marshal(map[string]any{
		"model": strings.TrimPrefix(prepared.Model, "ikun:"), "messages": messages,
		"max_tokens": prepared.MaxTokens, "stream": true,
		"tools": tools, "tool_choice": map[string]string{"type": "none"},
		"output_config": map[string]string{"effort": "low"},
	})
	if err != nil {
		return OutgoingRequest{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, credentials.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return OutgoingRequest{}, err
	}
	request.Header.Set("x-api-key", credentials.APIKey)
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "aether")
	return OutgoingRequest{Request: request, Body: body, PreventRedirect: true}, nil
}

// HasOutput 忽略思考和心跳；压缩不会建立用户可见的回复占位。
func (claudeProvider) HasOutput(payload []byte) bool {
	var event struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	return json.Unmarshal(payload, &event) == nil && event.Delta.Type == "text_delta" && strings.TrimSpace(event.Delta.Text) != ""
}

// DecodeStream 只接受完整结束的摘要，保留输入、缓存命中和输出用量；流被截断时不保存半份摘要。
func (claudeProvider) DecodeStream(payloads [][]byte, _ Request) (Response, bool, error) {
	var response Response
	var text strings.Builder
	var stopReason string
	var stopped bool
	for _, payload := range payloads {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					Input         *int32 `json:"input_tokens"`
					CacheRead     int32  `json:"cache_read_input_tokens"`
					CacheCreation int32  `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content_block"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				Output *int32 `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return response, false, fmt.Errorf("decode Claude stream event: %w", err)
		}
		switch event.Type {
		case "message_start":
			response.ID, response.Model = event.Message.ID, event.Message.Model
			if usage := event.Message.Usage; usage.Input != nil {
				total := int64(*usage.Input) + int64(usage.CacheRead) + int64(usage.CacheCreation)
				if total < 0 || total > 2147483647 {
					return response, false, fmt.Errorf("Claude returned invalid input usage")
				}
				value := int32(total)
				response.Usage.PromptTokens = &value
			}
		case "content_block_start":
			if event.ContentBlock.Type == "tool_use" {
				return response, false, fmt.Errorf("Claude returned a tool call while compaction tools are disabled")
			}
			if event.ContentBlock.Type == "text" {
				text.WriteString(event.ContentBlock.Text)
			}
		case "content_block_delta":
			if event.Delta.Type == "text_delta" {
				text.WriteString(event.Delta.Text)
			}
		case "message_delta":
			if event.Delta.StopReason != "" {
				stopReason = event.Delta.StopReason
			}
			if event.Usage.Output != nil {
				response.Usage.CompletionTokens = event.Usage.Output
			}
		case "message_stop":
			stopped = true
		case "error":
			return response, event.Error.Type == "overloaded_error" || event.Error.Type == "rate_limit_error" || event.Error.Type == "api_error", fmt.Errorf("Claude stream error: %s", event.Error.Type)
		}
	}
	if !stopped {
		return response, true, fmt.Errorf("Claude stream ended before completion")
	}
	if stopReason != "end_turn" && stopReason != "stop_sequence" {
		return response, false, fmt.Errorf("Claude response did not complete (stop reason: %s)", stopReason)
	}
	if strings.TrimSpace(text.String()) == "" {
		return response, false, fmt.Errorf("Claude returned no summary text")
	}
	message, _ := json.Marshal(map[string]string{"role": "assistant", "content": text.String()})
	response.Choices = append(response.Choices, struct {
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	}{message, "stop"})
	return response, false, nil
}
