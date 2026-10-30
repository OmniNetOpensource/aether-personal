// 本文件适配 ikun 的 Gemini 原生协议：转换消息与工具，保留原生思考签名，并把响应和用量交回共用 Agent 循环。

package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// geminiContent 保留每个 part 的原始 JSON，签名及其所在位置在工具续接和重启后都不变。
type geminiContent struct {
	Role  string            `json:"role"`
	Parts []json.RawMessage `json:"parts"`
}

// geminiFunctionCall 记录原生工具标识；没有 ID 的旧版模型按名称接收结果。
type geminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// geminiRequestBody 把运行时共用消息转为 generateContent 请求，不改动数据库中的历史。
func geminiRequestBody(prepared modelRequest) ([]byte, error) {
	contents := []geminiContent{}
	systemParts := []map[string]string{}
	toolCalls := map[string]geminiFunctionCall{}
	for _, raw := range prepared.Messages {
		var message struct {
			Role          string          `json:"role"`
			Content       json.RawMessage `json:"content"`
			ToolCalls     []toolCall      `json:"tool_calls"`
			ToolCallID    string          `json:"tool_call_id"`
			GeminiContent *geminiContent  `json:"gemini_content"`
			GeminiModel   string          `json:"gemini_model"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, fmt.Errorf("decode message for Gemini: %w", err)
		}
		var text string
		_ = json.Unmarshal(message.Content, &text)
		if message.Role == "system" {
			systemParts = append(systemParts, map[string]string{"text": text})
			continue
		}
		content := geminiContent{Role: "user", Parts: []json.RawMessage{}}
		switch message.Role {
		case "tool":
			call, exists := toolCalls[message.ToolCallID]
			if !exists {
				return nil, fmt.Errorf("Gemini history has a tool result without its call")
			}
			part, _ := json.Marshal(map[string]any{"functionResponse": map[string]any{
				"name": call.Name, "response": map[string]string{"output": text},
			}})
			if call.ID != "" {
				part, _ = json.Marshal(map[string]any{"functionResponse": map[string]any{
					"id": call.ID, "name": call.Name, "response": map[string]string{"output": text},
				}})
			}
			content.Parts = append(content.Parts, part)
		case "assistant", "user":
			if message.Role == "assistant" {
				content.Role = "model"
			}
			// 同一 Gemini 模型的输出原样续接；切换来源时只转换共用文本和工具轨迹。
			if message.Role == "assistant" && message.GeminiContent != nil && message.GeminiModel == prepared.Model {
				content = *message.GeminiContent
				// 存量原生内容里可能有只有签名、没有数据的空段（流式分片或旧响应都可能产生），回传前并掉，否则供应商会以缺少 data 拒绝整条请求。
				content.Parts = sanitizeGeminiParts(nil, content.Parts)
				index := 0
				for _, rawPart := range content.Parts {
					var part struct {
						Call *geminiFunctionCall `json:"functionCall"`
					}
					_ = json.Unmarshal(rawPart, &part)
					if part.Call != nil {
						toolCalls[message.ToolCalls[index].ID] = *part.Call
						index++
					}
				}
			} else {
				if text != "" {
					part, _ := json.Marshal(map[string]string{"text": text})
					content.Parts = append(content.Parts, part)
				} else if len(message.Content) > 0 && message.Content[0] == '[' {
					// 历史图片只能使用已携带的 base64 内容，不额外下载任意外部地址。
					var blocks []struct {
						Type     string `json:"type"`
						Text     string `json:"text"`
						ImageURL struct {
							URL string `json:"url"`
						} `json:"image_url"`
					}
					_ = json.Unmarshal(message.Content, &blocks)
					for _, block := range blocks {
						var part []byte
						switch block.Type {
						case "text":
							part, _ = json.Marshal(map[string]string{"text": block.Text})
						case "image_url":
							mimeType, data, ok := strings.Cut(strings.TrimPrefix(block.ImageURL.URL, "data:"), ";base64,")
							if !ok || !strings.HasPrefix(mimeType, "image/") {
								return nil, fmt.Errorf("Gemini history requires base64 image data URLs")
							}
							part, _ = json.Marshal(map[string]any{"inlineData": map[string]string{"mimeType": mimeType, "data": data}})
						default:
							return nil, fmt.Errorf("unsupported Gemini history content: %s", block.Type)
						}
						content.Parts = append(content.Parts, part)
					}
				}
				for _, call := range message.ToolCalls {
					function := geminiFunctionCall{ID: call.ID, Name: call.Function.Name, Args: json.RawMessage(call.Function.Arguments)}
					toolCalls[call.ID] = function
					// Google 官方为迁入其他模型的工具历史提供此签名占位值，不伪造原模型签名。
					part, err := json.Marshal(map[string]any{"functionCall": function, "thoughtSignature": "skip_thought_signature_validator"})
					if err != nil {
						return nil, fmt.Errorf("encode Gemini tool history: %w", err)
					}
					content.Parts = append(content.Parts, part)
				}
			}
		default:
			return nil, fmt.Errorf("unsupported Gemini history role: %s", message.Role)
		}
		// 并行工具的结果放在同一条 user 消息中，避免拆断一轮工具调用。
		if len(content.Parts) == 0 {
			continue
		}
		if len(contents) > 0 && contents[len(contents)-1].Role == content.Role {
			contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, content.Parts...)
		} else {
			contents = append(contents, content)
		}
	}

	// JSON Schema 直接使用现有工具定义；Subagent 的强制交付沿用 send_message 的指定工具选择。
	declarations := []map[string]any{}
	for _, raw := range prepared.Tools {
		var tool struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		_ = json.Unmarshal(raw, &tool)
		declarations = append(declarations, map[string]any{"name": tool.Function.Name, "description": tool.Function.Description, "parametersJsonSchema": tool.Function.Parameters})
	}
	generation := map[string]any{"maxOutputTokens": prepared.MaxTokens}
	// 对齐 Aether 的推理强度；压缩和动态摘要沿用共用请求指定的低强度。
	model := strings.TrimPrefix(prepared.Model, "ikun:")
	if strings.HasPrefix(model, "gemini-3") || strings.HasPrefix(model, "gemini-4") {
		level := "HIGH"
		if prepared.Reasoning["effort"] == "low" {
			level = "LOW"
		}
		generation["thinkingConfig"] = map[string]any{"thinkingLevel": level, "includeThoughts": true}
	} else if strings.HasPrefix(model, "gemini-2.5") {
		thinking := map[string]any{"includeThoughts": true}
		if prepared.Reasoning["effort"] == "low" {
			thinking["thinkingBudget"] = 128
		}
		generation["thinkingConfig"] = thinking
	}
	body := map[string]any{
		"contents":         contents,
		"generationConfig": generation,
	}
	if len(systemParts) > 0 {
		body["systemInstruction"] = map[string]any{"parts": systemParts}
	}
	if len(declarations) > 0 {
		body["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
	}
	if len(prepared.ToolChoice) > 0 {
		// 禁止工具的选择（如上下文压缩）翻成 NONE：即使请求里没有工具声明，也明确告诉模型不要输出工具调用；
		// 指定某个工具的选择仍按原来的 ANY 加白名单处理。
		var choiceMode string
		if json.Unmarshal(prepared.ToolChoice, &choiceMode) == nil && choiceMode == "none" {
			body["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "NONE"}}
		} else {
			var choice struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			_ = json.Unmarshal(prepared.ToolChoice, &choice)
			body["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "ANY", "allowedFunctionNames": []string{choice.Function.Name}}}
		}
	}
	return json.Marshal(body)
}

// decodeGeminiResponse 在供应商边界校验响应，先保留已产生的用量，再决定输出是否可执行。
func decodeGeminiResponse(body []byte, prepared modelRequest) (chatResponse, error) {
	var wire struct {
		ResponseID   string `json:"responseId"`
		ModelVersion string `json:"modelVersion"`
		Candidates   []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason"`
		} `json:"candidates"`
		Usage struct {
			PromptTokens     *int32 `json:"promptTokenCount"`
			CompletionTokens *int32 `json:"candidatesTokenCount"`
			ThinkingTokens   *int32 `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	var response chatResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return response, fmt.Errorf("decode Gemini response: %w", err)
	}
	response.ID, response.Model = wire.ResponseID, wire.ModelVersion
	response.Usage.PromptTokens = wire.Usage.PromptTokens
	response.Usage.CompletionTokens = wire.Usage.CompletionTokens
	if wire.Usage.CompletionTokens != nil && wire.Usage.ThinkingTokens != nil {
		total := int64(*wire.Usage.CompletionTokens) + int64(*wire.Usage.ThinkingTokens)
		if *wire.Usage.CompletionTokens < 0 || *wire.Usage.ThinkingTokens < 0 || total > 2147483647 {
			response.Usage.CompletionTokens = nil
		} else {
			value := int32(total)
			response.Usage.CompletionTokens = &value
		}
	}
	if len(wire.Candidates) == 0 {
		return response, fmt.Errorf("Gemini returned no candidates (block reason: %s)", wire.PromptFeedback.BlockReason)
	}
	candidate := wire.Candidates[0]
	if candidate.FinishReason != "STOP" || candidate.Content.Role != "model" {
		return response, fmt.Errorf("Gemini response did not complete (finish reason: %s)", candidate.FinishReason)
	}
	texts := []string{}
	calls := []map[string]any{}
	ids := map[string]bool{}
	for _, raw := range candidate.Content.Parts {
		var part struct {
			Text    string              `json:"text"`
			Thought bool                `json:"thought"`
			Call    *geminiFunctionCall `json:"functionCall"`
		}
		if err := json.Unmarshal(raw, &part); err != nil {
			return response, fmt.Errorf("decode Gemini content part: %w", err)
		}
		if !part.Thought && part.Text != "" {
			texts = append(texts, part.Text)
		}
		if part.Call != nil {
			var args map[string]json.RawMessage
			if part.Call.Name == "" || json.Unmarshal(part.Call.Args, &args) != nil || args == nil {
				return response, fmt.Errorf("Gemini returned an invalid function call")
			}
			id := part.Call.ID
			if id == "" {
				id = uuid.NewString()
			}
			if ids[id] {
				return response, fmt.Errorf("Gemini returned duplicate function call IDs")
			}
			ids[id] = true
			calls = append(calls, map[string]any{"id": id, "type": "function", "function": map[string]string{"name": part.Call.Name, "arguments": string(part.Call.Args)}})
		}
	}
	if len(texts) == 0 && len(calls) == 0 {
		return response, fmt.Errorf("Gemini returned no text or function calls")
	}
	message, _ := json.Marshal(map[string]any{
		"role": "assistant", "content": strings.Join(texts, ""), "tool_calls": calls,
		"gemini_content": candidate.Content, "gemini_model": prepared.Model,
	})
	finish := "stop"
	if len(calls) > 0 {
		finish = "tool_calls"
	}
	response.Choices = append(response.Choices, struct {
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	}{message, finish})
	return response, nil
}
