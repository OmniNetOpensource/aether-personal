// 本文件负责 Agent 的模型请求流程：组装提示词与上下文，经 providers 按模型自动选择渠道发送，记录实际请求响应与用量，并按可重试规则处理失败。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/providers"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/google/uuid"
)

// chatResponse 与 modelRequest 是 providers 统一响应与请求的别名：Agent 内部沿用这两个名字，渠道差异不进本包。
type chatResponse = providers.Response

// modelRequest 见 chatResponse 的说明。
type modelRequest = providers.Request

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

// agentRequest 组装一次 Agent 请求：按身份选提示词与工具，注入当前时间，并把历史里的压缩摘要转成用户消息。
func (r *Runner) agentRequest(address agentAddress, contextMessages []json.RawMessage, model string, outputLimit int32) modelRequest {
	prompt := mainPrompt
	if address.subagentID != nil {
		prompt = subagentPrompt
	}
	tools := toolDefinitions(address)
	prompt += fmt.Sprintf(currentTimePrompt, time.Now().UTC().Format(time.RFC3339))
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

// requestModel 发送一次模型请求并全程记账：渠道由 providers 按模型名自动选择，本函数只负责凭证、日志、传输与结果分类。
func (r *Runner) requestModel(ctx context.Context, prepared modelRequest, journal database.StartModelCallParams) (response chatResponse, retryable bool, err error) {
	// 渠道标识留在运行时与日志里，供应商只收到自己的模型名和协议。
	provider := providers.ForModel(prepared.Model)
	credentials, err := r.services.Load(ctx, provider.Service())
	if err != nil {
		return response, false, err
	}
	outgoing, err := provider.PrepareRequest(ctx, credentials, prepared)
	if err != nil {
		return response, false, err
	}
	request, body := outgoing.Request, outgoing.Body

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
	if outgoing.PreventRedirect {
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
		responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, providers.MaxResponseBytes+1))
		capturedResponseBody = responseBody
		responseTruncated = len(responseBody) > providers.MaxResponseBytes
		if responseTruncated {
			capturedResponseBody = responseBody[:providers.MaxResponseBytes]
		}
		if err != nil {
			return response, true, err
		}
		outcome = "http_error"
		retryable := httpResponse.StatusCode == http.StatusTooManyRequests || httpResponse.StatusCode >= 500
		return response, retryable, fmt.Errorf("model provider returned %d: %.500s", httpResponse.StatusCode, responseBody)
	}
	// 逐块读取事件流并在内存里拼成完整回复；日志存档保留实际收到的原始分片。
	payloads, captured, truncated, readErr := providers.ReadSSEPayloads(httpResponse.Body)
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
	response, incomplete, decodeErr = provider.DecodeStream(payloads, prepared)
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
