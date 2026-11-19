// 本文件是 agent loop 的请求阶段：发出请求前阶段组装好的请求并收回完整回复；
// 普通与压缩请求共用出站文本过滤；首段输出到达时插入占位，完整回复由请求后阶段保存。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/providers"
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
			// 固定生成这条回复时模型看过的消息边界，崩溃后也能重建 compact 的输入。
			if err := q.SetStepInputSequence(ctx, database.SetStepInputSequenceParams{ID: stepID, InputSequence: request.InputSequence}); err != nil {
				return err
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
	response, retryable, err := r.sendModelRequest(modelCtx, request, "agent", reserve)
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

// sendModelRequest 发送一次已组装好的模型请求并返回结果：渠道由 providers 按模型名自动选择，返回前清理供应商可能回传的负数用量。
func (r *Runner) sendModelRequest(ctx context.Context, prepared modelRequest, purpose string, onOutput func() error) (response chatResponse, retryable bool, err error) {
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
	request := outgoing.Request

	client := *r.modelClient
	if outgoing.PreventRedirect {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	httpResponse, err := client.Do(request)
	if err != nil {
		return response, true, err
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode != http.StatusOK {
		responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, providers.MaxResponseBytes+1))
		if len(responseBody) > providers.MaxResponseBytes {
			responseBody = responseBody[:providers.MaxResponseBytes]
		}
		if err != nil {
			return response, true, err
		}
		retryable := httpResponse.StatusCode == http.StatusTooManyRequests || httpResponse.StatusCode >= 500
		return response, retryable, fmt.Errorf("model provider returned %d: %.500s", httpResponse.StatusCode, responseBody)
	}
	var outputErr error
	payloads, _, truncated, readErr := providers.ReadSSEPayloads(httpResponse.Body, func(payload []byte) error {
		if onOutput != nil && provider.HasOutput(payload) {
			outputErr = onOutput()
			onOutput = nil
		}
		return outputErr
	})
	if truncated {
		return response, false, fmt.Errorf("model response exceeds 8 MiB")
	}
	if outputErr != nil {
		return response, false, outputErr
	}
	if readErr != nil {
		return response, true, readErr
	}
	var incomplete bool
	var decodeErr error
	response, incomplete, decodeErr = provider.DecodeStream(payloads, prepared)
	if response.Usage.PromptTokens != nil && *response.Usage.PromptTokens < 0 {
		response.Usage.PromptTokens = nil
	}
	if response.Usage.CompletionTokens != nil && *response.Usage.CompletionTokens < 0 {
		response.Usage.CompletionTokens = nil
	}
	if response.Usage.Cost != nil && response.Usage.Cost.IsNegative() {
		response.Usage.Cost = nil
	}
	if decodeErr != nil {
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
	if r.subagentID != nil && purpose == "agent" && len(message.ToolCalls) == 0 && AssistantTextToUser(response.Choices[0].Message) == "" {
		return response, true, errors.New("subagent returned empty final answer")
	}
	return response, false, nil
}
