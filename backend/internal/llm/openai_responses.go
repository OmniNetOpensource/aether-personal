// 本文件实现 OpenAI 原生 Responses API 协议：构造无状态流式请求（保留同模型原生输出与加密推理续接）、解码 SSE 响应并兼容 OAuth 与 API Key 两种 /models 目录格式。

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// prepareOpenAIResponsesRequest 将统一消息与工具定义转为 Responses API 的流式请求，同模型历史原样回放原生输出项以保住加密推理连续性。
func prepareOpenAIResponsesRequest(ctx context.Context, config Config, prepared Request) (OutgoingRequest, error) {
	inputItems := make([]any, 0, len(prepared.Messages))
	for _, raw := range prepared.Messages {
		var message struct {
			Role                  string            `json:"role"`
			Content               json.RawMessage   `json:"content"`
			ToolCalls             []historyToolCall `json:"tool_calls"`
			ToolCallID            string            `json:"tool_call_id"`
			OpenAIResponsesOutput []json.RawMessage `json:"openai_responses_output"`
			OpenAIResponsesModel  string            `json:"openai_responses_model"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return OutgoingRequest{}, fmt.Errorf("decode message for OpenAI Responses: %w", err)
		}
		// 同模型续接时原样回放上次返回的原生输出项（含 reasoning.encrypted_content、message 与 function_call 及原始 id）。
		if message.Role == "assistant" && message.OpenAIResponsesModel == prepared.Model && len(message.OpenAIResponsesOutput) > 0 {
			for _, item := range message.OpenAIResponsesOutput {
				inputItems = append(inputItems, item)
			}
			continue
		}
		switch message.Role {
		case "user":
			parts, err := encodeResponsesInputParts(message.Content)
			if err != nil {
				return OutgoingRequest{}, err
			}
			if len(parts) > 0 {
				inputItems = append(inputItems, map[string]any{
					"type":    "message",
					"role":    "user",
					"content": parts,
				})
			}
		case "assistant":
			text, err := decodePlainTextContent(message.Content)
			if err != nil {
				return OutgoingRequest{}, err
			}
			if text != "" {
				inputItems = append(inputItems, map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []map[string]string{
						{"type": "output_text", "text": text},
					},
				})
			}
			for _, call := range message.ToolCalls {
				args := call.Function.Arguments
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				inputItems = append(inputItems, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Function.Name,
					"arguments": args,
				})
			}
		case "tool":
			output, err := encodeResponsesToolOutput(message.Content)
			if err != nil {
				return OutgoingRequest{}, err
			}
			inputItems = append(inputItems, map[string]any{
				"type":    "function_call_output",
				"call_id": message.ToolCallID,
				"output":  output,
			})
		default:
			return OutgoingRequest{}, fmt.Errorf("unsupported OpenAI Responses history role: %s", message.Role)
		}
	}

	tools := make([]map[string]any, 0, len(prepared.Tools))
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
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        tool.Function.Name,
			"description": tool.Function.Description,
			"parameters":  tool.Function.Parameters,
		})
	}

	// store=false 保持无状态，并要求返回 reasoning.encrypted_content 供下一轮同模型无状态续接推理。
	bodyFields := map[string]any{
		"model":   prepared.Model,
		"input":   inputItems,
		"stream":  true,
		"store":   false,
		"include": []string{"reasoning.encrypted_content"},
	}
	if config.SendSessionID && prepared.SessionID != "" {
		bodyFields["prompt_cache_key"] = prepared.SessionID
	}
	if len(tools) > 0 {
		bodyFields["tools"] = tools
		if len(prepared.ToolChoice) > 0 {
			bodyFields["tool_choice"] = prepared.ToolChoice
		} else {
			bodyFields["tool_choice"] = "auto"
		}
	}
	if prepared.Reasoning != nil {
		bodyFields["reasoning"] = prepared.Reasoning
	}

	body, err := json.Marshal(bodyFields)
	if err != nil {
		return OutgoingRequest{}, err
	}
	endpoint := strings.TrimRight(config.BaseURL, "/") + "/responses"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return OutgoingRequest{}, err
	}
	request.Header.Set("Authorization", "Bearer "+config.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("User-Agent", "aether")
	return OutgoingRequest{Request: request, Body: body, PreventRedirect: true}, nil
}

// encodeResponsesInputParts 把通用用户消息 content（纯字符串或 text/image_url 数组）转为 Responses API 的输入块数组。
func encodeResponsesInputParts(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil, nil
		}
		return []map[string]any{{"type": "input_text", "text": text}}, nil
	}
	if raw[0] != '[' {
		return nil, fmt.Errorf("unsupported OpenAI Responses content payload")
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	parts := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, map[string]any{"type": "input_text", "text": block.Text})
		case "image_url":
			parts = append(parts, map[string]any{"type": "input_image", "image_url": block.ImageURL.URL})
		default:
			return nil, fmt.Errorf("unsupported OpenAI Responses content block: %s", block.Type)
		}
	}
	return parts, nil
}

// encodeResponsesToolOutput 转换工具回执内容：纯文本直接作为字符串，带截图的多模态数组转为 input_text / input_image 数组。
func encodeResponsesToolOutput(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	return encodeResponsesInputParts(raw)
}

// decodePlainTextContent 从跨模型历史的助手消息中提取纯文本正文，用于无原生输出时的通用回放。
func decodePlainTextContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	if raw[0] != '[' {
		return "", nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", err
	}
	var combined strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			combined.WriteString(block.Text)
		}
	}
	return combined.String(), nil
}

// openAIResponsesUsage 接收 Responses API 完成事件里的输入、缓存与输出 Token 计数。
type openAIResponsesUsage struct {
	InputTokens        *int32 `json:"input_tokens"`
	OutputTokens       *int32 `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens *int32 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// openAIResponsesError 提取 response.failed 或 error 事件中的错误码与说明，用于判断是否可重试。
type openAIResponsesError struct {
	Code    string `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// isRetryableResponsesError 判断流内错误是否属于服务端限流或临时故障。
func isRetryableResponsesError(code, errType string) bool {
	switch code {
	case "server_error", "rate_limit_exceeded", "overloaded", "timeout":
		return true
	}
	switch errType {
	case "server_error", "rate_limit_error", "overloaded_error":
		return true
	}
	return false
}

// decodeOpenAIResponsesStream 解析 Responses API SSE 事件，拼回统一 llm.Response 并保留原生 output 项供同模型续接。
func decodeOpenAIResponsesStream(payloads [][]byte, prepared Request) (Response, bool, error) {
	var response Response
	var deltaText strings.Builder
	var deltaReasoning strings.Builder
	doneItems := map[int]json.RawMessage{}
	var completedOutput []json.RawMessage
	completed := false

	for _, payload := range payloads {
		if string(payload) == "[DONE]" {
			continue
		}
		var event struct {
			Type        string                `json:"type"`
			Delta       string                `json:"delta"`
			OutputIndex int                   `json:"output_index"`
			Item        json.RawMessage       `json:"item"`
			Code        string                `json:"code"`
			Message     string                `json:"message"`
			Error       *openAIResponsesError `json:"error"`
			Response    *struct {
				ID                string                `json:"id"`
				Model             string                `json:"model"`
				Status            string                `json:"status"`
				Output            []json.RawMessage     `json:"output"`
				Usage             *openAIResponsesUsage `json:"usage"`
				Error             *openAIResponsesError `json:"error"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
			} `json:"response"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return response, false, fmt.Errorf("decode OpenAI Responses stream event: %w", err)
		}
		switch event.Type {
		case "response.created", "response.in_progress":
			if event.Response != nil {
				if event.Response.ID != "" {
					response.ID = event.Response.ID
				}
				if event.Response.Model != "" {
					response.Model = event.Response.Model
				}
			}
		case "response.output_text.delta":
			deltaText.WriteString(event.Delta)
		case "response.reasoning_summary_text.delta":
			deltaReasoning.WriteString(event.Delta)
		case "response.output_item.done":
			if len(event.Item) > 0 {
				doneItems[event.OutputIndex] = event.Item
			}
		case "response.completed":
			completed = true
			if event.Response != nil {
				if event.Response.ID != "" {
					response.ID = event.Response.ID
				}
				if event.Response.Model != "" {
					response.Model = event.Response.Model
				}
				if len(event.Response.Output) > 0 {
					completedOutput = event.Response.Output
				}
				applyResponsesUsage(&response, event.Response.Usage)
			}
		case "response.incomplete":
			reason := "unknown"
			if event.Response != nil {
				applyResponsesUsage(&response, event.Response.Usage)
				if event.Response.IncompleteDetails != nil && event.Response.IncompleteDetails.Reason != "" {
					reason = event.Response.IncompleteDetails.Reason
				}
			}
			return response, false, fmt.Errorf("OpenAI Responses did not complete (reason: %s)", reason)
		case "response.failed":
			code, errType, msg := "", "", "response failed"
			if event.Response != nil && event.Response.Error != nil {
				code, errType, msg = event.Response.Error.Code, event.Response.Error.Type, event.Response.Error.Message
			}
			return response, isRetryableResponsesError(code, errType), fmt.Errorf("OpenAI Responses failed (%s): %s", code, msg)
		case "error":
			code, errType, msg := event.Code, "", event.Message
			if event.Error != nil {
				if event.Error.Code != "" {
					code = event.Error.Code
				}
				errType = event.Error.Type
				if event.Error.Message != "" {
					msg = event.Error.Message
				}
			}
			return response, isRetryableResponsesError(code, errType), fmt.Errorf("OpenAI Responses stream error (%s): %s", code, msg)
		}
	}

	if !completed {
		return response, true, fmt.Errorf("OpenAI Responses stream ended before completion")
	}

	// 优先使用 response.completed 给出的权威 output 列表；部分兼容端省略时退回按序号拼出的 output_item.done。
	nativeOutput := completedOutput
	if len(nativeOutput) == 0 && len(doneItems) > 0 {
		indexes := make([]int, 0, len(doneItems))
		for index := range doneItems {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		nativeOutput = make([]json.RawMessage, 0, len(indexes))
		for _, index := range indexes {
			nativeOutput = append(nativeOutput, doneItems[index])
		}
	}

	var textBuilder strings.Builder
	var reasoningBuilder strings.Builder
	toolCalls := []map[string]any{}
	for _, rawItem := range nativeOutput {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
			Summary []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return response, false, fmt.Errorf("decode OpenAI Responses output item: %w", err)
		}
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text", "text":
					textBuilder.WriteString(part.Text)
				case "refusal":
					textBuilder.WriteString(part.Refusal)
				}
			}
		case "reasoning":
			for _, part := range item.Summary {
				if part.Text != "" {
					if reasoningBuilder.Len() > 0 {
						reasoningBuilder.WriteString("\n")
					}
					reasoningBuilder.WriteString(part.Text)
				}
			}
		case "function_call":
			if string(prepared.ToolChoice) == `"none"` {
				return response, false, fmt.Errorf("OpenAI Responses returned a tool call while tools are disabled")
			}
			args := item.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			if item.CallID == "" || item.Name == "" || !json.Valid([]byte(args)) {
				return response, false, fmt.Errorf("OpenAI Responses returned an invalid tool call")
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   item.CallID,
				"type": "function",
				"function": map[string]string{
					"name":      item.Name,
					"arguments": args,
				},
			})
		}
	}

	text := textBuilder.String()
	if text == "" {
		text = deltaText.String()
	}
	reasoning := reasoningBuilder.String()
	if reasoning == "" {
		reasoning = deltaReasoning.String()
	}
	if string(prepared.ToolChoice) == `"none"` && strings.TrimSpace(text) == "" {
		return response, false, fmt.Errorf("OpenAI Responses returned no summary text")
	}

	messageFields := map[string]any{
		"role":                    "assistant",
		"content":                 text,
		"tool_calls":              toolCalls,
		"openai_responses_output": nativeOutput,
		"openai_responses_model":  prepared.Model,
	}
	if reasoning != "" {
		messageFields["reasoning"] = reasoning
	}
	message, err := json.Marshal(messageFields)
	if err != nil {
		return response, false, err
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	response.Choices = append(response.Choices, struct {
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
	}{message, finishReason})
	return response, false, nil
}

// applyResponsesUsage 将 Responses API 的用量字段映射到统一的 llm.Response.Usage。
func applyResponsesUsage(response *Response, usage *openAIResponsesUsage) {
	if usage == nil {
		return
	}
	response.Usage.PromptTokens = usage.InputTokens
	response.Usage.CompletionTokens = usage.OutputTokens
	response.Usage.CachedTokens = nil
	if usage.InputTokensDetails != nil {
		response.Usage.CachedTokens = usage.InputTokensDetails.CachedTokens
	}
}

// listOpenAIResponsesModels 读取 {BaseURL}/models 目录，同时兼容 ChatGPT OAuth 的 models[] 格式与标准 OpenAI 的 data[] 格式。
func listOpenAIResponsesModels(ctx context.Context, client *http.Client, config Config) ([]CatalogModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	endpoint := strings.TrimRight(config.BaseURL, "/") + "/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+config.APIKey)
	request.Header.Set("User-Agent", "aether")
	httpClient := noRedirectClient(client)
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog returned HTTP %d", response.StatusCode)
	}
	const maxCatalogSize = 16 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCatalogSize {
		return nil, errors.New("model catalog is too large")
	}
	var catalog struct {
		Models []struct {
			Slug          string `json:"slug"`
			DisplayName   string `json:"display_name"`
			Visibility    string `json:"visibility"`
			ContextWindow int64  `json:"context_window"`
		} `json:"models"`
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int64  `json:"context_length"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var models []CatalogModel
	if len(catalog.Models) > 0 {
		for _, entry := range catalog.Models {
			if entry.Slug == "" || len(entry.Slug) > 160 || seen[entry.Slug] {
				continue
			}
			// ChatGPT OAuth 目录用 visibility 区分用户可见模型与内部模型。
			if entry.Visibility != "" && entry.Visibility != "list" {
				continue
			}
			seen[entry.Slug] = true
			name := entry.DisplayName
			if name == "" {
				name = entry.Slug
			}
			model := CatalogModel{ID: entry.Slug, Name: name}
			if entry.ContextWindow > 0 {
				length := entry.ContextWindow
				model.ContextLength = &length
			}
			models = append(models, model)
		}
	} else {
		for _, entry := range catalog.Data {
			if entry.ID == "" || len(entry.ID) > 160 || seen[entry.ID] {
				continue
			}
			seen[entry.ID] = true
			name := entry.Name
			if name == "" {
				name = entry.ID
			}
			model := CatalogModel{ID: entry.ID, Name: name}
			if entry.ContextLength > 0 {
				length := entry.ContextLength
				model.ContextLength = &length
			}
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		return nil, errors.New("model catalog has no model candidates")
	}
	return models, nil
}
