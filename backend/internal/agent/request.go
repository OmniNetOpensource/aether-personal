// 本文件是 agent loop 的请求阶段：发出请求前阶段组装好的请求，收回完整回复并写下这次调用的日志；
// 普通与压缩请求共用出站文本过滤；首段输出到达时插入占位，完整回复由请求后阶段保存。

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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// chatResponse 与 modelRequest 是 providers 统一响应与请求的别名：Agent 内部沿用这两个名字，渠道差异不进本包。
type chatResponse = providers.Response

// modelRequest 见 chatResponse 的说明。
type modelRequest = providers.Request

// sendRequest 是请求阶段的入口：Main 在思考阶段可被用户新输入取消，首段输出时校验无新输入后插入占位；
// Subagent 独立在后台运行，不受用户新消息打断，仅核对自身租约与取消状态后插入占位。
func (r *Runner) sendRequest(ctx, budget context.Context, stepID uuid.UUID, request modelRequest) (uuid.UUID, chatResponse, bool, error) {
	modelCtx, cancel := context.WithCancelCause(budget)
	defer cancel(nil)
	stopListening := func() {}
	if r.subagentID == nil {
		r.mutex.Lock()
		r.thinking = &thinkingRequest{request.InputSequence, cancel}
		r.mutex.Unlock()
		stopListening = func() {
			r.mutex.Lock()
			r.thinking = nil
			r.mutex.Unlock()
		}
	}
	defer stopListening()

	input := database.HasNewMainInputParams{UserID: r.userID, Through: request.InputSequence}
	var replyID uuid.UUID
	reserve := func() error {
		stopListening()
		return pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
			q := database.New(tx)
			if err := r.lockAndVerifyLease(ctx, q); err != nil {
				if errors.Is(err, errLostLease) {
					cancel(errLostLease)
				}
				return err
			}
			if r.subagentID == nil {
				newer, err := q.HasNewMainInput(ctx, input)
				if err != nil {
					return err
				}
				if newer {
					cancel(errNewInput)
					return errNewInput
				}
			}
			var err error
			replyID, err = q.ReserveMainReply(ctx, database.ReserveMainReplyParams{
				UserID:     r.userID,
				SubagentID: r.subagentID,
				StepID:     &stepID,
			})
			return err
		})
	}
	response, retryable, err := r.sendModelRequest(modelCtx, request,
		database.StartModelCallParams{StepID: stepID, Purpose: "agent"}, reserve)
	if errors.Is(context.Cause(modelCtx), errLostLease) || errors.Is(err, errLostLease) {
		return uuid.Nil, response, false, errLostLease
	}
	if errors.Is(context.Cause(modelCtx), errNewInput) {
		return uuid.Nil, response, false, errNewInput
	}
	if err != nil {
		return uuid.Nil, response, retryable, err
	}
	if replyID == uuid.Nil {
		if err := reserve(); err != nil {
			if errors.Is(context.Cause(modelCtx), errLostLease) || errors.Is(err, errLostLease) {
				return uuid.Nil, response, false, errLostLease
			}
			if errors.Is(context.Cause(modelCtx), errNewInput) {
				return uuid.Nil, response, false, errNewInput
			}
			return uuid.Nil, response, false, err
		}
	}
	return replyID, response, false, nil
}

// sendModelRequest 发送一次已组装好的模型请求并返回结果：渠道由 providers 按模型名自动选择，终态日志在返回前由 finishModelCall 落库。
func (r *Runner) sendModelRequest(ctx context.Context, prepared modelRequest, journal database.StartModelCallParams, onOutput func() error) (response chatResponse, retryable bool, err error) {
	// 先过滤请求中的文本，再展开图片并转换为渠道协议。普通请求与压缩请求共用这条路径。
	filtered, err := filterModelMessages(prepared.Messages)
	if err != nil {
		return response, false, err
	}
	resolved, err := r.toolDeps.Images.ResolveMessages(ctx, filtered)
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
		if finishErr := r.finishModelCall(ctx, callID, startedAt, outcome, status, capturedResponseBody, responseTruncated, &response); finishErr != nil {
			err, retryable = finishErr, false
		}
	}()
	client := *r.modelClient
	if outgoing.PreventRedirect {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	httpResponse, err := client.Do(request)
	if err != nil {
		return response, true, err
	}
	defer httpResponse.Body.Close()
	statusCode := int32(httpResponse.StatusCode)
	status = &statusCode
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
		Role      string     `json:"role"`
		ToolCalls []toolCall `json:"tool_calls"`
	}
	if json.Unmarshal(response.Choices[0].Message, &message) != nil || message.Role != "assistant" {
		return response, false, fmt.Errorf("model provider returned invalid assistant message")
	}
	// Subagent 的职责是交付完整调研回答，不允许以无工具且空白的正文静默收尾；空结果按可重试处理。
	if r.subagentID != nil && journal.Purpose == "agent" && len(message.ToolCalls) == 0 && AssistantTextToUser(response.Choices[0].Message) == "" {
		return response, true, errors.New("subagent returned empty final answer")
	}
	outcome = "succeeded"
	return response, false, nil
}

// finishModelCall 给一次模型调用落终态日志：先清掉供应商回的负数用量，再按结果分类、耗时与截获的响应体写入 model_calls。
func (r *Runner) finishModelCall(ctx context.Context, callID uuid.UUID, startedAt time.Time, outcome string, status *int32, capturedResponseBody []byte, responseTruncated bool, response *chatResponse) error {
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
		return nil
	}
	duration := time.Since(startedAt).Milliseconds()
	var durationMS *int64 = &duration
	// 模型预算耗尽或 Subagent 15 分钟上限按超时保留耗时；进程退出、主动取消或执行权丢失按外部中断记录。
	if ctx.Err() != nil && !errors.Is(context.Cause(ctx), errModelRetryTimeout) && !errors.Is(context.Cause(ctx), errSubagentRunTimeout) {
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
		return fmt.Errorf("finish model call journal: %w", finishErr)
	}
	if rows != 1 {
		return errors.New("model call execution ownership was recovered")
	}
	return nil
}
