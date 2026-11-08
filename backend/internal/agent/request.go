// 本文件是 agent loop 的请求阶段：发出请求前阶段组装好的请求，收回完整回复并写下这次调用的日志；
// 首段输出到达时插入占位定下回复的位置，回复本身由请求后阶段保存。压缩请求也经 sendModelRequest 发送。

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
	"github.com/jackc/pgx/v5"
)

// chatResponse 与 modelRequest 是 providers 统一响应与请求的别名：Agent 内部沿用这两个名字，渠道差异不进本包。
type chatResponse = providers.Response

// modelRequest 见 chatResponse 的说明。
type modelRequest = providers.Request

// sendRequest 是请求阶段的入口：模型还没有产出正文或工具时，新输入可以取消这次请求；首段输出到达时插入占位，
// 占位与用户输入插入用同一把事务锁确定先后，较晚完成的回复因此不会排到它没读过的消息后面。返回占位 ID 与完整回复。
func (r *Runner) sendRequest(ctx, budget context.Context, stepID uuid.UUID, request modelRequest) (uuid.UUID, chatResponse, bool, error) {
	modelCtx, cancel := context.WithCancelCause(budget)
	defer cancel(nil)
	r.mutex.Lock()
	r.thinking = &thinkingRequest{request.InputSequence, cancel}
	r.mutex.Unlock()
	stopListening := func() {
		r.mutex.Lock()
		r.thinking = nil
		r.mutex.Unlock()
	}
	defer stopListening()

	// 组装之后、登记之前到达的输入不会触发取消，由占位事务里的这次检查兜住，只是放弃得晚一些。
	input := database.HasNewMainInputParams{UserID: r.userID, Through: request.InputSequence}
	var replyID uuid.UUID
	reserve := func() error {
		stopListening()
		return pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
			q := database.New(tx)
			if err := q.LockUserMessages(ctx, r.userID); err != nil {
				return err
			}
			newer, err := q.HasNewMainInput(ctx, input)
			if err != nil {
				return err
			}
			if newer {
				cancel(errNewInput)
				return errNewInput
			}
			replyID, err = q.ReserveMainReply(ctx, database.ReserveMainReplyParams{UserID: r.userID, StepID: &stepID})
			return err
		})
	}
	response, retryable, err := r.sendModelRequest(modelCtx, request,
		database.StartModelCallParams{StepID: stepID, Purpose: "agent"}, reserve)
	if errors.Is(context.Cause(modelCtx), errNewInput) {
		return uuid.Nil, response, false, errNewInput
	}
	if err != nil {
		return uuid.Nil, response, retryable, err
	}
	// 合法的沉默回复也在同一写入锁下与新输入交接。
	if replyID == uuid.Nil {
		if err := reserve(); err != nil {
			if errors.Is(context.Cause(modelCtx), errNewInput) {
				return uuid.Nil, response, false, errNewInput
			}
			return uuid.Nil, response, false, err
		}
	}
	return replyID, response, false, nil
}

// sendModelRequest 发送一次已组装好的模型请求并返回结果：渠道由 providers 按模型名自动选择，本函数只负责凭证、传输与结果分类，终态日志在返回前由 finishModelCall 落库。
func (r *Runner) sendModelRequest(ctx context.Context, prepared modelRequest, journal database.StartModelCallParams, onOutput func() error) (response chatResponse, retryable bool, err error) {
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
		if finishErr := r.finishModelCall(ctx, callID, startedAt, outcome, status, capturedResponseBody, responseTruncated, &response); finishErr != nil {
			err, retryable = finishErr, false
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
		Role      string     `json:"role"`
		ToolCalls []toolCall `json:"tool_calls"`
	}
	if json.Unmarshal(response.Choices[0].Message, &message) != nil || message.Role != "assistant" {
		return response, false, fmt.Errorf("model provider returned invalid assistant message")
	}
	outcome = "succeeded"
	return response, false, nil
}

// finishModelCall 给一次模型调用落终态日志：先清掉供应商回的负数用量，再按结果分类、耗时与截获的响应体写入 model_calls，供看板排查与费用统计；
// 调用未登记时只做清洗。返回非空错误表示日志没写成，调用方据此覆盖本次请求的结果（执行权可能已被收回）。
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
		return fmt.Errorf("finish model call journal: %w", finishErr)
	}
	if rows != 1 {
		return errors.New("model call execution ownership was recovered")
	}
	return nil
}
