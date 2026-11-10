// 本文件适配 ikun Claude 原生 Messages 接口，供 Main 与压缩共用；保留原生内容和思考签名，转换工具调用并记录流式用量。

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

// claudeProvider 承接 ikun Claude 的普通对话与上下文压缩。
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

// PrepareRequest 转换共用消息与工具定义；同模型的原生内容完整回传，只有压缩请求禁用工具。
func (claudeProvider) PrepareRequest(ctx context.Context, credentials services.Settings, prepared Request) (OutgoingRequest, error) {
	messages := []claudeMessage{}
	for _, raw := range prepared.Messages {
		var message struct {
			Role          string            `json:"role"`
			Content       json.RawMessage   `json:"content"`
			ToolCalls     []historyToolCall `json:"tool_calls"`
			ToolCallID    string            `json:"tool_call_id"`
			ClaudeContent []json.RawMessage `json:"claude_content"`
			ClaudeModel   string            `json:"claude_model"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return OutgoingRequest{}, fmt.Errorf("decode message for Claude: %w", err)
		}
		// 同一模型直接回传原生块，保住思考签名和正文、工具的交错顺序；其他模型的历史只转换共用字段。
		if message.Role == "assistant" && message.ClaudeModel == prepared.Model && len(message.ClaudeContent) > 0 {
			messages = append(messages, claudeMessage{Role: "assistant", Content: message.ClaudeContent})
			continue
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
	// ikun 允许省略 max_tokens，由渠道决定输出额度；Main 与压缩共用此行为。
	// Main 使用模型默认推理设置并允许工具；压缩沿用统一请求给出的低强度与禁用工具设置。
	bodyFields := map[string]any{
		"model": strings.TrimPrefix(prepared.Model, "ikun:"), "messages": messages,
		"stream": true,
	}
	if len(tools) > 0 {
		choice := "auto"
		if string(prepared.ToolChoice) == `"none"` {
			choice = "none"
		}
		bodyFields["tools"] = tools
		bodyFields["tool_choice"] = map[string]string{"type": choice}
	}
	if effort, ok := prepared.Reasoning["effort"].(string); ok {
		bodyFields["output_config"] = map[string]string{"effort": effort}
	}
	body, err := json.Marshal(bodyFields)
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

// HasOutput 在首段正文或工具调用到达时占位；思考与心跳仍允许新输入取消请求。
func (claudeProvider) HasOutput(payload []byte) bool {
	var event struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content_block"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return false
	}
	return (event.Type == "content_block_start" && (event.ContentBlock.Type == "tool_use" || (event.ContentBlock.Type == "text" && strings.TrimSpace(event.ContentBlock.Text) != ""))) ||
		(event.Type == "content_block_delta" && event.Delta.Type == "text_delta" && strings.TrimSpace(event.Delta.Text) != "")
}

// claudeStreamBlock 累积一个内容块；原始字段保留供应商元数据，四个 Builder 分别承接对应的流式增量。
type claudeStreamBlock struct {
	fields                           map[string]json.RawMessage
	text, thinking, signature, input strings.Builder
}

// DecodeStream 按内容块拼回正文、思考签名与工具参数，原生块用于下一轮完整续接，共用字段供运行时执行与看板读取。
func (claudeProvider) DecodeStream(payloads [][]byte, prepared Request) (Response, bool, error) {
	var response Response
	// 原始字段保留供应商附加元数据；增量用 Builder 累积，避免每个分片都复制整段正文或签名。
	var blocks []*claudeStreamBlock
	var stopReason string
	var stopped bool
	for _, payload := range payloads {
		var event struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					Input         *int32 `json:"input_tokens"`
					CacheRead     int32  `json:"cache_read_input_tokens"`
					CacheCreation int32  `json:"cache_creation_input_tokens"`
					Output        *int32 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock map[string]json.RawMessage `json:"content_block"`
			Delta        struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
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
			response.Usage.CompletionTokens = event.Message.Usage.Output
			if usage := event.Message.Usage; usage.Input != nil {
				total := int64(*usage.Input) + int64(usage.CacheRead) + int64(usage.CacheCreation)
				if total < 0 || total > 2147483647 {
					return response, false, fmt.Errorf("Claude returned invalid input usage")
				}
				value := int32(total)
				response.Usage.PromptTokens = &value
			}
		case "content_block_start":
			if event.Index != len(blocks) || event.ContentBlock == nil {
				return response, false, fmt.Errorf("Claude returned an invalid content block index")
			}
			blocks = append(blocks, &claudeStreamBlock{fields: event.ContentBlock})
			block := blocks[event.Index]
			for field, builder := range map[string]*strings.Builder{"text": &block.text, "thinking": &block.thinking, "signature": &block.signature} {
				if raw, ok := block.fields[field]; ok {
					var value string
					if err := json.Unmarshal(raw, &value); err != nil {
						return response, false, err
					}
					builder.WriteString(value)
				}
			}
		case "content_block_delta":
			if event.Index < 0 || event.Index >= len(blocks) {
				return response, false, fmt.Errorf("Claude delta has no content block")
			}
			block := blocks[event.Index]
			switch event.Delta.Type {
			case "text_delta":
				block.text.WriteString(event.Delta.Text)
			case "thinking_delta":
				block.thinking.WriteString(event.Delta.Thinking)
			case "signature_delta":
				block.signature.WriteString(event.Delta.Signature)
			case "input_json_delta":
				block.input.WriteString(event.Delta.PartialJSON)
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
	if stopReason != "end_turn" && stopReason != "stop_sequence" && stopReason != "tool_use" && stopReason != "refusal" {
		return response, false, fmt.Errorf("Claude response did not complete (stop reason: %s)", stopReason)
	}
	// 原生块和共用字段来自同一份完整响应；部分生成的工具参数不能进入执行阶段。
	var text, reasoning strings.Builder
	nativeContent := []json.RawMessage{}
	toolCalls := []map[string]any{}
	for index := range blocks {
		block := blocks[index]
		for field, builder := range map[string]*strings.Builder{"text": &block.text, "thinking": &block.thinking, "signature": &block.signature} {
			if _, ok := block.fields[field]; ok || builder.Len() > 0 {
				block.fields[field], _ = json.Marshal(builder.String())
			}
		}
		if block.input.Len() > 0 {
			block.fields["input"] = json.RawMessage(block.input.String())
		}
		raw, err := json.Marshal(block.fields)
		if err != nil {
			return response, false, fmt.Errorf("encode Claude content block: %w", err)
		}
		nativeContent = append(nativeContent, raw)
		var content struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &content); err != nil {
			return response, false, err
		}
		switch content.Type {
		case "text":
			text.WriteString(block.text.String())
		case "thinking":
			reasoning.WriteString(block.thinking.String())
		case "tool_use":
			if string(prepared.ToolChoice) == `"none"` {
				return response, false, fmt.Errorf("Claude returned a tool call while compaction tools are disabled")
			}
			var arguments map[string]json.RawMessage
			if err := json.Unmarshal(content.Input, &arguments); err != nil || arguments == nil || content.ID == "" || content.Name == "" {
				return response, false, fmt.Errorf("Claude returned an invalid tool call")
			}
			toolCalls = append(toolCalls, map[string]any{
				"id": content.ID, "type": "function",
				"function": map[string]string{"name": content.Name, "arguments": string(content.Input)},
			})
		}
	}
	if string(prepared.ToolChoice) == `"none"` && strings.TrimSpace(text.String()) == "" {
		return response, false, fmt.Errorf("Claude returned no summary text")
	}
	messageFields := map[string]any{
		"role": "assistant", "content": text.String(), "tool_calls": toolCalls,
		"claude_content": nativeContent, "claude_model": prepared.Model,
	}
	if reasoning.Len() > 0 {
		messageFields["reasoning"] = reasoning.String()
	}
	message, err := json.Marshal(messageFields)
	if err != nil {
		return response, false, err
	}
	finishReason := "stop"
	if stopReason == "tool_use" {
		finishReason = "tool_calls"
	}
	response.Choices = append(response.Choices, struct {
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	}{message, finishReason})
	return response, false, nil
}
