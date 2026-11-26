// 本文件实现 Gemini 原生协议：转换消息与工具、保留原生思考签名、解码流式分片并读取 /v1beta/models 目录。

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// prepareGeminiRequest 将统一请求转为 Gemini 的流式 generateContent 请求，直接使用上游真实模型名。
func prepareGeminiRequest(ctx context.Context, config Config, prepared Request) (OutgoingRequest, error) {
	endpoint := strings.TrimRight(config.BaseURL, "/") + "/v1beta/models/" + url.PathEscape(prepared.Model) + ":streamGenerateContent?alt=sse"
	body, err := geminiRequestBody(prepared)
	if err != nil {
		return OutgoingRequest{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return OutgoingRequest{}, err
	}
	request.Header.Set("x-goog-api-key", config.APIKey)
	request.Header.Set("User-Agent", "aether")
	request.Header.Set("Content-Type", "application/json")
	return OutgoingRequest{Request: request, Body: body, PreventRedirect: true}, nil
}

// geminiOutputPart 用于解析 Gemini 流式与完整回复的分片，思考片段通过 thought 区分。
type geminiOutputPart struct {
	Text    string              `json:"text"`
	Thought bool                `json:"thought"`
	Call    *geminiFunctionCall `json:"functionCall"`
}

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

// geminiToolOutput 把工具回执拆成文本和图片。字符串回执只有文本；内容数组里的 image_url 必须已是 data URL。
func geminiToolOutput(raw json.RawMessage) (string, []map[string]any, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil, nil
	}
	if len(raw) == 0 || raw[0] != '[' {
		return "", nil, fmt.Errorf("unsupported Gemini tool result content")
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil, err
	}
	texts := []string{}
	media := []map[string]any{}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		case "image_url":
			mimeType, data, ok := strings.Cut(strings.TrimPrefix(block.ImageURL.URL, "data:"), ";base64,")
			if !ok || !strings.HasPrefix(mimeType, "image/") {
				return "", nil, fmt.Errorf("Gemini history requires base64 image data URLs")
			}
			media = append(media, map[string]any{"inlineData": map[string]string{"mimeType": mimeType, "data": data}})
		default:
			return "", nil, fmt.Errorf("unsupported Gemini tool result content: %s", block.Type)
		}
	}
	return strings.Join(texts, "\n"), media, nil
}

// geminiRequestBody 把运行时共用消息转为 generateContent 请求，不改动数据库中的历史。
func geminiRequestBody(prepared Request) ([]byte, error) {
	contents := []geminiContent{}
	toolCalls := map[string]geminiFunctionCall{}
	previousMessageRole := ""
	for _, raw := range prepared.Messages {
		var message struct {
			Role          string            `json:"role"`
			Content       json.RawMessage   `json:"content"`
			ToolCalls     []historyToolCall `json:"tool_calls"`
			ToolCallID    string            `json:"tool_call_id"`
			GeminiContent *geminiContent    `json:"gemini_content"`
			GeminiModel   string            `json:"gemini_model"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, fmt.Errorf("decode message for Gemini: %w", err)
		}
		var text string
		_ = json.Unmarshal(message.Content, &text)
		content := geminiContent{Role: "user", Parts: []json.RawMessage{}}
		switch message.Role {
		case "tool":
			call, exists := toolCalls[message.ToolCallID]
			if !exists {
				return nil, fmt.Errorf("Gemini history has a tool result without its call")
			}
			output, media, err := geminiToolOutput(message.Content)
			if err != nil {
				return nil, err
			}
			// 文本放进 response.output；图片放进 functionResponse.parts，Gemini 3 起才能看见工具回执里的图。
			functionResponse := map[string]any{"name": call.Name, "response": map[string]string{"output": output}}
			if call.ID != "" {
				functionResponse["id"] = call.ID
			}
			if len(media) > 0 {
				functionResponse["parts"] = media
			}
			part, err := json.Marshal(map[string]any{"functionResponse": functionResponse})
			if err != nil {
				return nil, fmt.Errorf("encode Gemini tool result: %w", err)
			}
			content.Parts = append(content.Parts, part)
		case "assistant", "user":
			if message.Role == "assistant" {
				content.Role = "model"
			}
			// 同一 Gemini 模型的输出原样续接；切换模型时只转换共用文本和工具轨迹。
			if message.Role == "assistant" && message.GeminiContent != nil && message.GeminiModel == prepared.Model {
				content = *message.GeminiContent
				// 存量原生内容里可能有只有签名、没有数据的空段，回传前并掉，否则会被拒绝。
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
		// 只合并来源角色相同的相邻消息：并行工具回执成组，用户正文与时间成组。
		// tool 虽然也转成 user，但后续用户输入必须另起一条，避免埋在工具结果里被忽略。
		if len(content.Parts) == 0 {
			continue
		}
		if len(contents) > 0 && previousMessageRole == message.Role {
			contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, content.Parts...)
		} else {
			contents = append(contents, content)
		}
		previousMessageRole = message.Role
	}

	// JSON Schema 直接使用现有工具定义；指定工具选择沿用同一份工具定义转换。
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
	// 省略输出额度参数，普通回复与压缩都由供应商决定额度。
	generation := map[string]any{}
	model := prepared.Model
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
	if len(declarations) > 0 {
		body["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
	}
	if len(prepared.ToolChoice) > 0 {
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

// decodeGeminiResponse 校验合并后的 Gemini 响应，先保留已产生的用量，再决定输出是否可执行。
func decodeGeminiResponse(body []byte, prepared Request) (Response, error) {
	var wire struct {
		ResponseID   string `json:"responseId"`
		ModelVersion string `json:"modelVersion"`
		Candidates   []struct {
			Content      geminiContent `json:"content"`
			FinishReason string        `json:"finishReason"`
		} `json:"candidates"`
		Usage struct {
			PromptTokens     *int32 `json:"promptTokenCount"`
			CachedTokens     *int32 `json:"cachedContentTokenCount"`
			CompletionTokens *int32 `json:"candidatesTokenCount"`
			ThinkingTokens   *int32 `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	var response Response
	if err := json.Unmarshal(body, &wire); err != nil {
		return response, fmt.Errorf("decode Gemini response: %w", err)
	}
	response.ID, response.Model = wire.ResponseID, wire.ModelVersion
	response.Usage.PromptTokens = wire.Usage.PromptTokens
	response.Usage.CachedTokens = wire.Usage.CachedTokens
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
		var part geminiOutputPart
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
	// 空格与换行不算正文；Main 是否能安静结束由共用请求层判断，压缩始终需要摘要。
	text := strings.Join(texts, "")
	if strings.TrimSpace(text) == "" {
		text = ""
	}
	if text == "" && len(calls) == 0 && len(prepared.ToolChoice) > 0 {
		return response, fmt.Errorf("Gemini returned no text or function calls")
	}
	message, _ := json.Marshal(map[string]any{
		"role": "assistant", "content": text, "tool_calls": calls,
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

// sanitizeGeminiParts 把后续 parts 续到已累积的 parts 上，并清理只有 thoughtSignature、正文为空的空段：
// 这种段单独回传给 Gemini 会被拒绝，所以把签名并到前一个分片上；流式拼接与存量原生内容回传共用这套清理。
func sanitizeGeminiParts(accumulated, incoming []json.RawMessage) []json.RawMessage {
	for _, raw := range incoming {
		var part map[string]json.RawMessage
		if json.Unmarshal(raw, &part) != nil {
			accumulated = append(accumulated, raw)
			continue
		}
		hasData := false
		for key, value := range part {
			if key == "thoughtSignature" || key == "thought" {
				continue
			}
			if key == "text" {
				var text string
				if json.Unmarshal(value, &text) == nil && text == "" {
					continue
				}
			}
			hasData = true
		}
		if hasData {
			accumulated = append(accumulated, raw)
			continue
		}
		signature, ok := part["thoughtSignature"]
		if !ok || len(accumulated) == 0 {
			continue
		}
		var previous map[string]json.RawMessage
		if json.Unmarshal(accumulated[len(accumulated)-1], &previous) != nil {
			continue
		}
		previous["thoughtSignature"] = signature
		merged, err := json.Marshal(previous)
		if err != nil {
			continue
		}
		accumulated[len(accumulated)-1] = merged
	}
	return accumulated
}

// decodeGeminiStream 把 Gemini 的流式分片合并成一条完整的原生响应，再交给完整响应解析。
// 整条流没有给出结束原因说明流被截断，按可重试失败处理。
func decodeGeminiStream(payloads [][]byte, prepared Request) (Response, bool, error) {
	var responseID, modelVersion, finishReason string
	var parts []json.RawMessage
	var usage, promptFeedback json.RawMessage
	for _, payload := range payloads {
		var chunk struct {
			ResponseID   string `json:"responseId"`
			ModelVersion string `json:"modelVersion"`
			Candidates   []struct {
				Content      geminiContent `json:"content"`
				FinishReason string        `json:"finishReason"`
			} `json:"candidates"`
			Usage          json.RawMessage `json:"usageMetadata"`
			PromptFeedback json.RawMessage `json:"promptFeedback"`
		}
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return Response{}, false, fmt.Errorf("decode Gemini stream chunk: %w", err)
		}
		if chunk.ResponseID != "" {
			responseID = chunk.ResponseID
		}
		if chunk.ModelVersion != "" {
			modelVersion = chunk.ModelVersion
		}
		if len(chunk.Usage) > 0 {
			usage = chunk.Usage
		}
		if len(chunk.PromptFeedback) > 0 {
			promptFeedback = chunk.PromptFeedback
		}
		for _, candidate := range chunk.Candidates {
			if candidate.FinishReason != "" {
				finishReason = candidate.FinishReason
			}
			parts = sanitizeGeminiParts(parts, candidate.Content.Parts)
		}
	}
	merged := map[string]any{
		"responseId":   responseID,
		"modelVersion": modelVersion,
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": finishReason,
		}},
	}
	if len(usage) > 0 {
		merged["usageMetadata"] = usage
	}
	if len(promptFeedback) > 0 {
		merged["promptFeedback"] = promptFeedback
	}
	body, err := json.Marshal(merged)
	if err != nil {
		return Response{}, false, err
	}
	response, decodeErr := decodeGeminiResponse(body, prepared)
	if finishReason == "" {
		return response, true, fmt.Errorf("Gemini stream ended before completion")
	}
	return response, false, decodeErr
}

// listGeminiModels 分页读取 Gemini /v1beta/models 目录，返回不含渠道前缀的原生模型名与已知输入容量。
func listGeminiModels(ctx context.Context, client *http.Client, config Config) ([]CatalogModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	baseURL := strings.TrimRight(config.BaseURL, "/")
	httpClient := noRedirectClient(client)
	models := []CatalogModel{}
	seenPages := map[string]bool{}
	seenModels := map[string]bool{}
	pageToken := ""
	for {
		endpoint, err := url.Parse(baseURL + "/v1beta/models")
		if err != nil {
			return nil, err
		}
		query := endpoint.Query()
		query.Set("pageSize", "1000")
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		endpoint.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("x-goog-api-key", config.APIKey)
		request.Header.Set("User-Agent", "aether")
		response, err := httpClient.Do(request)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
		response.Body.Close()
		if response.StatusCode != http.StatusOK || readErr != nil || len(body) > 16<<20 {
			return nil, fmt.Errorf("Gemini model catalog unavailable (HTTP %d)", response.StatusCode)
		}
		var page struct {
			Models []struct {
				Name            string `json:"name"`
				DisplayName     string `json:"displayName"`
				InputTokenLimit int64  `json:"inputTokenLimit"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode Gemini model catalog: %w", err)
		}
		for _, entry := range page.Models {
			name := strings.TrimPrefix(entry.Name, "models/")
			if !strings.HasPrefix(name, "gemini-") || strings.ContainsAny(name, "/?#:") || len(name) > 155 || seenModels[name] {
				continue
			}
			seenModels[name] = true
			displayName := entry.DisplayName
			if displayName == "" || utf8.RuneCountInString(displayName) > 180 {
				displayName = name
			}
			model := CatalogModel{ID: name, Name: displayName}
			if entry.InputTokenLimit > 0 {
				limit := entry.InputTokenLimit
				model.ContextLength = &limit
			}
			models = append(models, model)
		}
		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
		if seenPages[pageToken] {
			return nil, fmt.Errorf("Gemini model catalog repeated a page token")
		}
		seenPages[pageToken] = true
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("Gemini model catalog has no agent model candidates")
	}
	return models, nil
}
