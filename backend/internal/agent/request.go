// 本文件负责 Agent 的模型请求流程：组装提示词与上下文，经 providers 按模型选择渠道，首段正文或工具调用交运行时占位，并记录完整请求响应与用量。

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
	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	"github.com/google/uuid"
)

// chatResponse 与 modelRequest 是 providers 统一响应与请求的别名：Agent 内部沿用这两个名字，渠道差异不进本包。
type chatResponse = providers.Response

// modelRequest 见 chatResponse 的说明。
type modelRequest = providers.Request

// agentRequest 组装一次 Agent 请求：指令注入加按组装规则排好的当前世代消息，发出前先过工具配对与尾巴检查。
func (r *Runner) agentRequest(rows []database.ListAgentContextRowsRow, model string, outputLimit int32) (modelRequest, error) {
	messages := buildRequestMessages(rows)
	if err := validateRequestMessages(messages); err != nil {
		return modelRequest{}, err
	}
	// 只取实际组装的这批行的最大序号；摘要排在首位，其物理序号可能大于保留的原文。
	var inputSequence int64
	for _, row := range rows {
		inputSequence = max(inputSequence, row.Sequence)
	}
	return modelRequest{InputSequence: inputSequence, Model: model, Messages: messages, Tools: tools.Definitions(), MaxTokens: outputLimit}, nil
}

// requestModel 发送一次模型请求并全程记账：渠道由 providers 按模型名自动选择，本函数只负责凭证、日志、传输与结果分类。
func (r *Runner) requestModel(ctx context.Context, prepared modelRequest, journal database.StartModelCallParams, onOutput func() error) (response chatResponse, retryable bool, err error) {
	// 渠道标识留在运行时与日志里，供应商只收到自己的模型名和协议。
	resolved, err := (images.Store{Services: r.services}).ResolveMessages(ctx, prepared.Messages)
	if err != nil {
		return response, false, err
	}
	prepared.Messages = resolved
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
					toolNames = append(toolNames, tools.RecordedName(call.Function.Name))
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
	// 仅在首次有效输出时通知运行时占位；压缩请求和已经占位的请求直接继续收流。
	var outputErr error
	payloads, captured, truncated, readErr := providers.ReadSSEPayloads(httpResponse.Body, func(payload []byte) error {
		if onOutput != nil && provider.HasOutput(payload) {
			outputErr = onOutput()
			onOutput = nil
		}
		return outputErr
	})
	capturedResponseBody, responseTruncated = captured, truncated
	if truncated {
		outcome = "invalid_response"
		return response, false, fmt.Errorf("model response exceeds 8 MiB")
	}
	if outputErr != nil {
		outcome = "interrupted"
		return response, false, outputErr
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
