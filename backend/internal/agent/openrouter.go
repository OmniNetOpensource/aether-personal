// 本文件负责共用模型请求流程：选择 OpenRouter 或 ikun Gemini，记录实际请求响应与用量，并按可重试规则处理失败。

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type chatResponse struct {
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

func (r *Runner) callModel(ctx context.Context, address agentAddress, contextMessages []json.RawMessage, stepIDs ...uuid.UUID) (response chatResponse, retryable bool, err error) {
	model := r.model(address)
	outputLimit := r.settings.MaxOutputTokens
	if outputLimit == 0 {
		outputLimit = config.DefaultMaxOutputTokens
	}
	if len(stepIDs) > 0 {
		settings, loadErr := modelsettings.Load(ctx, database.New(r.databasePool), r.settings)
		if loadErr != nil {
			return response, false, fmt.Errorf("load active model settings: %w", loadErr)
		}
		model = settings.MainModel
		outputLimit = settings.MainOutputLimit
		if address.subagentID != nil {
			model = settings.EffectiveSubagentModel
			outputLimit = settings.SubagentOutputLimit
		}
	}
	prepared := r.agentRequest(address, contextMessages, model, outputLimit)
	var stepID uuid.UUID
	if len(stepIDs) > 0 {
		stepID = stepIDs[0]
	}
	return r.requestModel(ctx, prepared, database.StartModelCallParams{StepID: stepID, Purpose: "agent"})
}

type modelRequest struct {
	Reasoning     map[string]any    `json:"reasoning,omitempty"`
	Model         string            `json:"model"`
	Messages      []json.RawMessage `json:"messages"`
	Tools         []json.RawMessage `json:"tools,omitempty"`
	ToolChoice    json.RawMessage   `json:"tool_choice,omitempty"`
	MaxTokens     int32             `json:"max_tokens"`
	Stream        bool              `json:"stream,omitempty"`
	StreamOptions map[string]bool   `json:"stream_options,omitempty"`
}

func (r *Runner) agentRequest(address agentAddress, contextMessages []json.RawMessage, model string, outputLimit int32) modelRequest {
	prompt := mainPrompt
	if address.subagentID != nil {
		prompt = subagentPrompt
	}
	tools := toolDefinitions(address)
	prompt += "\nCurrent time (UTC): " + time.Now().UTC().Format(time.RFC3339) + "\n"
	system, _ := json.Marshal(map[string]string{"role": "system", "content": prompt})
	messages := []json.RawMessage{system}
	for _, message := range contextMessages {
		var summary struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if json.Unmarshal(message, &summary) == nil && summary.Name == "context_summary" {
			message, _ = json.Marshal(map[string]string{"role": "user", "name": "context_summary", "content": summary.Content})
		}
		messages = append(messages, message)
	}

	var toolChoice json.RawMessage
	delivery := inspectDelivery(contextMessages, address)
	if delivery.corrected && !delivery.satisfied {
		toolChoice = json.RawMessage(`{"type":"function","function":{"name":"send_message"}}`)
	}
	return modelRequest{Model: model, Messages: messages, Tools: tools, ToolChoice: toolChoice, MaxTokens: outputLimit}
}

func (r *Runner) requestModel(ctx context.Context, prepared modelRequest, journal database.StartModelCallParams) (response chatResponse, retryable bool, err error) {
	// 渠道标识留在运行时与日志里，供应商只收到自己的模型名和协议。
	gemini := strings.HasPrefix(prepared.Model, "ikun:gemini-")
	service := services.OpenRouter
	if gemini {
		service = services.IkunGemini
	}
	credentials, err := r.services.Load(ctx, service)
	if err != nil {
		return response, false, err
	}
	endpoint, apiKey := "https://openrouter.ai/api/v1/chat/completions", credentials.APIKey
	var body []byte
	if gemini {
		// 流式端点逐块返回 SSE 事件，后端拼完才落库与推送，前端仍一次性收到整条回复。
		endpoint = credentials.BaseURL + "/v1beta/models/" + url.PathEscape(strings.TrimPrefix(prepared.Model, "ikun:")) + ":streamGenerateContent?alt=sse"
		body, err = geminiRequestBody(prepared)
	} else {
		// 要求流式返回，并在末尾分片带上用量；用量拿不到时记空，不阻断回复。
		prepared.Stream = true
		prepared.StreamOptions = map[string]bool{"include_usage": true}
		// 从 Gemini 切回 OpenRouter 时不向其他供应商泄漏原生协议元数据。
		messages := make([]json.RawMessage, len(prepared.Messages))
		copy(messages, prepared.Messages)
		for index, raw := range messages {
			if !bytes.Contains(raw, []byte(`"gemini_content"`)) {
				continue
			}
			var message map[string]json.RawMessage
			_ = json.Unmarshal(raw, &message)
			delete(message, "gemini_content")
			delete(message, "gemini_model")
			messages[index], _ = json.Marshal(message)
		}
		prepared.Messages = messages
		body, err = json.Marshal(prepared)
	}
	if err != nil {
		return response, false, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return chatResponse{}, false, err
	}
	if gemini {
		request.Header.Set("x-goog-api-key", apiKey)
		request.Header.Set("User-Agent", "aether")
	} else {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	request.Header.Set("Content-Type", "application/json")

	journal.RequestedModel, journal.RequestBody = prepared.Model, body
	callID, err := database.New(r.databasePool).StartModelCall(ctx, journal)
	if err != nil {
		return response, false, fmt.Errorf("start model call journal: %w", err)
	}
	startedAt := time.Now()
	outcome := "transport_error"
	var status *int32
	var capturedResponseBody []byte
	var responseTruncated bool
	defer func() {
		if response.Usage.PromptTokens != nil && *response.Usage.PromptTokens < 0 {
			response.Usage.PromptTokens = nil
		}
		if response.Usage.CompletionTokens != nil && *response.Usage.CompletionTokens < 0 {
			response.Usage.CompletionTokens = nil
		}
		if response.Usage.Cost != nil && response.Usage.Cost.IsNegative() {
			response.Usage.Cost = nil
		}
		if callID == uuid.Nil {
			return
		}
		duration := time.Since(startedAt).Milliseconds()
		var durationMS *int64 = &duration
		// 模型预算耗尽按传输超时保留耗时；进程退出或执行权丢失仍按外部中断记录。
		if ctx.Err() != nil && !errors.Is(context.Cause(ctx), errModelRetryTimeout) {
			outcome, durationMS = "interrupted", nil
		}
		var failure *string
		if outcome != "succeeded" {
			failure = &outcome
		}
		toolNames := []string{}
		if len(response.Choices) > 0 {
			var message assistantMessage
			if json.Unmarshal(response.Choices[0].Message, &message) == nil {
				for _, call := range message.ToolCalls {
					toolNames = append(toolNames, recordedToolName(call.Function.Name))
				}
			}
		}
		var responseModel, generationID *string
		if response.Model != "" && utf8.RuneCountInString(response.Model) <= 256 {
			responseModel = &response.Model
		}
		if response.ID != "" && utf8.RuneCountInString(response.ID) <= 256 {
			generationID = &response.ID
		}
		persistCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		rows, finishErr := database.New(r.databasePool).FinishModelCall(persistCtx, database.FinishModelCallParams{
			ID: callID, Outcome: outcome, HttpStatus: status, DurationMs: durationMS,
			ResponseModel: responseModel, ProviderGenerationID: generationID,
			InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens,
			Cost: response.Usage.Cost, ToolNames: toolNames, FailureKind: failure,
			ResponseBody: capturedResponseBody, ResponseTruncated: responseTruncated,
		})
		if finishErr != nil {
			err, retryable = fmt.Errorf("finish model call journal: %w", finishErr), false
		} else if rows != 1 {
			err, retryable = fmt.Errorf("model call execution ownership was recovered"), false
		}
	}()
	client := *r.modelClient
	if gemini {
		// 自定义密钥头不跟随重定向，避免被 HTTP 客户端转发给其他主机。
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	httpResponse, err := client.Do(request)
	if err != nil {
		return response, true, err
	}
	defer httpResponse.Body.Close()
	statusCode := int32(httpResponse.StatusCode)
	status = &statusCode
	// 出错时供应商返回的通常是普通 JSON 而非事件流，整段读回即可拿到错误信息。
	if httpResponse.StatusCode != http.StatusOK {
		responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxModelResponseBytes+1))
		capturedResponseBody = responseBody
		responseTruncated = len(responseBody) > maxModelResponseBytes
		if responseTruncated {
			capturedResponseBody = responseBody[:maxModelResponseBytes]
		}
		if err != nil {
			return response, true, err
		}
		outcome = "http_error"
		retryable := httpResponse.StatusCode == http.StatusTooManyRequests || httpResponse.StatusCode >= 500
		return response, retryable, fmt.Errorf("model provider returned %d: %.500s", httpResponse.StatusCode, responseBody)
	}
	// 逐块读取事件流并在内存里拼成完整回复；日志存档保留实际收到的原始分片。
	payloads, captured, truncated, readErr := readSSEPayloads(httpResponse.Body)
	capturedResponseBody, responseTruncated = captured, truncated
	if truncated {
		outcome = "invalid_response"
		return response, false, fmt.Errorf("model response exceeds 8 MiB")
	}
	if readErr != nil {
		outcome = "transport_error"
		return response, true, readErr
	}
	// Preserve provider usage independently of assistant validity and later tool execution.
	outcome = "invalid_response"
	var incomplete bool
	var decodeErr error
	if gemini {
		response, incomplete, decodeErr = assembleGeminiStream(payloads, prepared)
	} else {
		response, incomplete, decodeErr = assembleOpenRouterStream(payloads)
	}
	if decodeErr != nil {
		if incomplete {
			outcome = "transport_error"
		}
		return response, incomplete, decodeErr
	}
	if len(response.Choices) == 0 {
		return response, false, fmt.Errorf("model provider returned no choices")
	}
	var message struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(response.Choices[0].Message, &message) != nil || message.Role != "assistant" {
		return response, false, fmt.Errorf("model provider returned invalid assistant message")
	}
	outcome = "succeeded"
	return response, false, nil
}
