// 本文件负责模型流式响应的接收与拼接：逐块读取供应商的 SSE 事件流，在内存里拼成一条完整的助手回复，拼完才交回共用请求流程落库与推送。

package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// maxModelResponseBytes 是流式分片累计的上限，与此前一次性读取的 8 MiB 上限一致，超出按无效响应处理。
const maxModelResponseBytes = 8 << 20

// readSSEPayloads 逐行读取 SSE 事件流：把每个事件的 data 行收集为一条载荷，同时保留原始字节供模型调用日志存档。
// 返回的 truncated 表示累计原始字节超过上限；流被中途断开时读错误原样返回，调用方按可重试的传输失败处理。
func readSSEPayloads(body io.Reader) (payloads [][]byte, captured []byte, truncated bool, err error) {
	reader := bufio.NewReaderSize(body, 32*1024)
	var raw bytes.Buffer
	var dataLines []string
	// flushEvent 在事件之间的空行处把已攒的 data 行合成一条完整载荷。
	flushEvent := func() {
		if len(dataLines) == 0 {
			return
		}
		payloads = append(payloads, []byte(strings.Join(dataLines, "\n")))
		dataLines = nil
	}
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			raw.WriteString(line)
			if raw.Len() > maxModelResponseBytes {
				return payloads, raw.Bytes()[:maxModelResponseBytes], true, nil
			}
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				flushEvent()
			case strings.HasPrefix(trimmed, "data:"):
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			}
		}
		if readErr == io.EOF {
			flushEvent()
			return payloads, raw.Bytes(), false, nil
		}
		if readErr != nil {
			return payloads, raw.Bytes(), false, readErr
		}
	}
}

// openRouterStreamChunk 是流式分片的形状：正文与工具参数都只给增量片段，用量通常只在末尾分片出现。
type openRouterStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   json.RawMessage `json:"content"`
			ToolCalls []struct {
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
func assembleOpenRouterStream(payloads [][]byte) (chatResponse, bool, error) {
	var response chatResponse
	var content strings.Builder
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
	message, err := json.Marshal(map[string]any{"role": "assistant", "content": content.String(), "tool_calls": toolCalls})
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

// sanitizeGeminiParts 把后续 parts 续到已累积的 parts 上，并清理只有 thoughtSignature、正文为空的空段：
// 这种段单独回传给 Gemini 会被拒绝，所以把签名并到前一个分片上；流式拼接与存量原生内容回传共用这同一套清理。
func sanitizeGeminiParts(accumulated, incoming []json.RawMessage) []json.RawMessage {
	for _, raw := range incoming {
		var part map[string]json.RawMessage
		if json.Unmarshal(raw, &part) != nil {
			accumulated = append(accumulated, raw)
			continue
		}
		// 判断这一段有没有真正的内容：除签名与 thought 标记外没有任何数据字段，或只有空文本，都算空段。
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
		// 空段只保留它的签名：并到前一段上；前面还没有任何段时直接丢弃。
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

// assembleGeminiStream 把 Gemini 的流式分片合并成一条完整的原生响应，再交给原有的完整响应解析。
// 整条流没有给出结束原因说明流被截断，按可重试失败处理；最终解析沿用 decodeGeminiResponse 的校验。
func assembleGeminiStream(payloads [][]byte, prepared modelRequest) (chatResponse, bool, error) {
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
			return chatResponse{}, false, fmt.Errorf("decode Gemini stream chunk: %w", err)
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
	// 合并后的形状与非流式 generateContent 响应一致：文本分片成为并列的文本段，解析时按序拼接。
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
		return chatResponse{}, false, err
	}
	response, decodeErr := decodeGeminiResponse(body, prepared)
	if finishReason == "" {
		return response, true, fmt.Errorf("Gemini stream ended before completion")
	}
	return response, false, decodeErr
}
