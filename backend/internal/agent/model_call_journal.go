// 本文件负责模型调用的终态日志：一次请求结束后把结果分类、耗时、用量与截获的响应体写入 model_calls，供看板排查与费用统计。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
)

// finishModelCall 给一次模型调用落终态日志：先清掉供应商回的负数用量，再按结果分类、耗时与截获的响应体写入 model_calls；
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
