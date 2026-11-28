// 本文件实现 OpenAI Chat Completions 兼容协议：按传入配置组装流式请求、解码响应（含推理与费用扩展）并读取 /models 目录。

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// prepareOpenAIRequest 将统一请求序列化为流式 Chat Completions 请求，并剥离属于其他原生协议的历史元数据。
func prepareOpenAIRequest(ctx context.Context, config Config, prepared Request) (OutgoingRequest, error) {
	prepared.Stream = true
	prepared.StreamOptions = map[string]bool{"include_usage": true}
	if !config.SendSessionID {
		prepared.SessionID = ""
	}
	if config.EnableReasoning {
		if strings.HasPrefix(prepared.Model, "google/gemini-") && prepared.Reasoning == nil {
			prepared.Reasoning = map[string]any{"effort": "high", "exclude": false}
		}
	} else {
		prepared.Reasoning = nil
	}
	// 从原生 Gemini / Claude / OpenAI Responses 切到 OpenAI Chat Completions 时不泄漏原生元数据字段。
	messages := make([]json.RawMessage, len(prepared.Messages))
	copy(messages, prepared.Messages)
	for index, raw := range messages {
		if !bytes.Contains(raw, []byte(`"gemini_content"`)) &&
			!bytes.Contains(raw, []byte(`"claude_content"`)) &&
			!bytes.Contains(raw, []byte(`"openai_responses_output"`)) {
			continue
		}
		var message map[string]json.RawMessage
		_ = json.Unmarshal(raw, &message)
		delete(message, "gemini_content")
		delete(message, "gemini_model")
		delete(message, "claude_content")
		delete(message, "claude_model")
		delete(message, "openai_responses_output")
		delete(message, "openai_responses_model")
		messages[index], _ = json.Marshal(message)
	}
	prepared.Messages = messages
	body, err := json.Marshal(prepared)
	if err != nil {
		return OutgoingRequest{}, err
	}
	endpoint := strings.TrimRight(config.BaseURL, "/") + "/chat/completions"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return OutgoingRequest{}, err
	}
	request.Header.Set("Authorization", "Bearer "+config.APIKey)
	request.Header.Set("Content-Type", "application/json")
	return OutgoingRequest{Request: request, Body: body, PreventRedirect: true}, nil
}

// openAIStreamChunk 接收正文、推理和工具调用的流式分片，用量通常只在末尾分片出现。
type openAIStreamChunk struct {
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
		PromptTokens        *int32 `json:"prompt_tokens"`
		CompletionTokens    *int32 `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens *int32 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		Cost *decimal.Decimal `json:"cost"`
	} `json:"usage"`
}

// streamToolCall 按序号累积一条工具调用：名称与 ID 只在首个分片出现，参数靠后续分片逐段拼接。
type streamToolCall struct {
	id        string
	name      string
	arguments strings.Builder
}

// decodeOpenAIStream 把 OpenAI 兼容的 SSE 分片拼成完整响应。
// 没收到 [DONE] 或结束原因说明流被截断，按可重试失败处理；分片不是合法 JSON 时按无效响应处理。
func decodeOpenAIStream(payloads [][]byte) (Response, bool, error) {
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
		var chunk openAIStreamChunk
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
			response.Usage.CachedTokens = nil
			if chunk.Usage.PromptTokensDetails != nil {
				response.Usage.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
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
	// 推理与正文一起落进助手消息；下一轮原样回传，看板可直接读取文本或加密状态。
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

// streamContentText 提取一个分片的正文增量：通常是字符串，少数兼容端用分段数组时拼接其中的文本段。
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

// listOpenAIModels 读取 OpenAI 兼容端点的 /models 目录；若条目声明了 supported_parameters 则仅保留支持 tools 的模型。
func listOpenAIModels(ctx context.Context, client *http.Client, config Config) ([]CatalogModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	endpoint := strings.TrimRight(config.BaseURL, "/") + "/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+config.APIKey)
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
		Data []struct {
			ID                  string   `json:"id"`
			Name                string   `json:"name"`
			ContextLength       int64    `json:"context_length"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var models []CatalogModel
	for _, entry := range catalog.Data {
		if entry.ID == "" || len(entry.ID) > 160 || seen[entry.ID] {
			continue
		}
		if len(entry.SupportedParameters) > 0 && !slices.Contains(entry.SupportedParameters, "tools") {
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
	if len(models) == 0 {
		return nil, errors.New("model catalog has no model candidates")
	}
	return models, nil
}
