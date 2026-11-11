// 本文件是 agent loop 的请求后阶段：处理本轮的结局。成功就保存回复并执行其中的工具；被新输入取代或遇到临时故障就安排下一轮；
// 其余失败（包括前两个阶段交来的错误）在这里统一收尾：补中断回执、结束步骤、记下失败位置。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// afterRequest 是请求后阶段的入口：err 是本轮在请求前或请求阶段遇到的错误，为空表示拿到了完整回复。
// 返回值告诉循环主体下一步：本轮完成、原样重来一轮，或结束本次运行；只有失败收尾本身出错才返回错误。
func (r *Runner) afterRequest(ctx context.Context, retry *modelRetry, through int64, stepID, replyID uuid.UUID, response chatResponse, retryable bool, err error) (roundEnd, error) {
	// 成功：回复与步骤同一事务落库，再执行回复里的工具；回执齐全后由下一轮按消息尾部继续回复。
	if err == nil {
		retry.reset()
		if err := r.saveReply(ctx, stepID, replyID, response); err != nil {
			return r.failRun(ctx, through, err)
		}
		var message assistantMessage
		_ = json.Unmarshal(response.Choices[0].Message, &message)
		if err := r.executeToolCalls(ctx, stepID, message.ToolCalls); err != nil {
			return r.failRun(ctx, through, err)
		}
		return roundDone, nil
	}
	if ctx.Err() != nil {
		return roundStop, nil
	}
	// 被更新的输入取代：撤销未发布的占位并结束这一步，清零重试后按消息尾部重来，新输入会一并带上。
	if errors.Is(err, errNewInput) {
		if err := r.abandonAttempt(ctx, stepID, errNewInput.Error()); err != nil {
			return r.failRun(ctx, through, err)
		}
		retry.reset()
		return roundDone, nil
	}
	if retry.budget.Err() != nil {
		err = fmt.Errorf("%w: %w", context.Cause(retry.budget), err)
	}
	// 网络错误、超时、429 与 5xx 等临时故障：这一步以错误收尾，退避 1、2、4 秒后原样再走一轮，最多再试 3 次，且不超出十分钟预算。
	if !retryable || retry.retries >= 3 || retry.budget.Err() != nil {
		return r.failRun(ctx, through, err)
	}
	slog.Error("model request failed", "user_id", r.userID, "attempt", retry.retries+1, "error", err)
	if err := r.abandonAttempt(ctx, stepID, err.Error()); err != nil {
		return r.failRun(ctx, through, err)
	}
	delay := time.Second << retry.retries
	retry.retries++
	select {
	case <-retry.budget.Done():
		return r.failRun(ctx, through, context.Cause(retry.budget))
	case <-time.After(delay):
		return roundRetry, nil
	}
}

// abandonAttempt 放弃这一次尝试：撤销未发布的占位，以原因结束这一步；下一轮的尝试会重新开一步。
func (r *Runner) abandonAttempt(ctx context.Context, stepID uuid.UUID, reason string) error {
	queries := database.New(r.databasePool)
	if err := queries.DiscardGeneratingReply(ctx, r.userID); err != nil {
		return err
	}
	return queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, Error: &reason})
}

// saveReply 在一个事务里完成助手记录与步骤：正文独立成为用户回复，有正文才对用户可见。工具调用从此成为历史的一部分。
func (r *Runner) saveReply(ctx context.Context, stepID, replyID uuid.UUID, response chatResponse) error {
	return pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, r.userID); err != nil {
			return err
		}
		if err := queries.CompleteMainReply(ctx, database.CompleteMainReplyParams{ID: replyID, Content: response.Choices[0].Message, Visible: AssistantTextToUser(response.Choices[0].Message) != ""}); err != nil {
			return err
		}
		if err := queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens, Cost: response.Usage.Cost}); err != nil {
			return err
		}
		return queries.NotifyUserMessages(ctx, r.userID)
	})
}

// executeToolCalls 并行执行独立工具，电脑操作则在一个协程里按模型原始顺序执行。
// 请求前阶段也复用它补跑缺少回执的调用；外部操作中断后可能重跑，回执保存成功后不会重复执行。
func (r *Runner) executeToolCalls(ctx context.Context, stepID uuid.UUID, calls []toolCall) error {
	toolErrors := make([]error, len(calls))
	// 每个执行单元独占自己的结果槽，数据库连接仅在短事务期间使用。
	execute := func(index int) {
		call := calls[index]
		if tool, ok := tools.ByName(call.Function.Name); ok && tool.OutsideTransaction {
			result, err := tool.Execute(ctx, r.toolDeps, nil, r.userID, call.Function.Arguments)
			if err != nil {
				if ctx.Err() != nil {
					toolErrors[index] = err
					return
				}
				result = "Error: " + err.Error()
			}
			toolErrors[index] = r.saveToolResult(ctx, stepID, call, result)
			return
		}
		toolErrors[index] = r.executeDatabaseToolAndSaveResult(ctx, stepID, call)
	}
	var waitGroup sync.WaitGroup
	ordered := make([]int, 0, len(calls))
	for index, call := range calls {
		if tool, ok := tools.ByName(call.Function.Name); ok && tool.Ordered {
			ordered = append(ordered, index)
			continue
		}
		waitGroup.Go(func() { execute(index) })
	}
	// 先保留原始下标再启动唯一电脑队列；不能用多个协程抢锁，否则执行顺序仍不确定。
	if len(ordered) > 0 {
		waitGroup.Go(func() {
			for _, index := range ordered {
				if ctx.Err() != nil {
					return
				}
				execute(index)
				if toolErrors[index] != nil {
					return
				}
			}
		})
	}
	waitGroup.Wait()
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	for _, err := range toolErrors {
		if err != nil {
			return err
		}
	}
	return nil
}

// toolResultContent 把一个工具调用的结果写成原生 tool 消息，靠 tool_call_id 与调用对应。
// 带 ContentArray 的工具成功时保存文本与 oss:// 图片段，带 UserVisibleParts 的工具成功时附带展示用的 parts 并标为对聊天可见。
func toolResultContent(call toolCall, result string) ([]byte, *string, bool, error) {
	var content any = result
	trimmed := strings.TrimSpace(result)
	tool, ok := tools.ByName(call.Function.Name)
	if ok && tool.UserVisibleParts && strings.HasPrefix(trimmed, "{") {
		var visible struct {
			Content string          `json:"content"`
			Parts   json.RawMessage `json:"parts"`
		}
		if json.Unmarshal([]byte(trimmed), &visible) == nil && len(visible.Parts) > 0 {
			sender := "main"
			encoded, err := json.Marshal(map[string]any{"role": "tool", "tool_call_id": call.ID, "content": visible.Content, "parts": visible.Parts})
			return encoded, &sender, true, err
		}
	}
	if ok && tool.ContentArray && strings.HasPrefix(trimmed, "[") {
		content = json.RawMessage(trimmed)
	}
	encoded, err := json.Marshal(map[string]any{"role": "tool", "tool_call_id": call.ID, "content": content})
	return encoded, nil, false, err
}

// saveToolResult 保存单个工具的回执，让中断恢复时能按回执判断这个工具已经执行过；
// 工具并行执行时从连接池取连接保存，同一用户的插入由 LockUserMessages 在数据库内串行排队。
func (r *Runner) saveToolResult(ctx context.Context, stepID uuid.UUID, call toolCall, result string) error {
	content, sender, visible, err := toolResultContent(call, result)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, r.userID); err != nil {
			return err
		}
		if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: r.userID, Role: "tool", Content: content, StepID: &stepID, Sender: sender, Visible: visible}); err != nil {
			return err
		}
		return queries.NotifyUserMessages(ctx, r.userID)
	})
}

// executeDatabaseTool 执行与回执同一事务的工具。未登记的名字返回工具错误，让模型可以改口。
func (r *Runner) executeDatabaseTool(ctx context.Context, queries *database.Queries, call toolCall) (string, error) {
	tool, ok := tools.ByName(call.Function.Name)
	if !ok {
		return "Error: tool not available: " + call.Function.Name, nil
	}
	return tool.Execute(ctx, r.toolDeps, queries, r.userID, call.Function.Arguments)
}

// executeDatabaseToolAndSaveResult 在一个事务里执行单个数据库工具并保存它的回执：
// 工具的写入与回执要么一起提交、要么一起回滚，所以恢复时看到有回执就代表工具已经生效，不会重复执行。
// 每个工具从连接池取自己的连接开事务，同一批工具才能真正并行；同一用户的消息插入由 LockUserMessages 串行排队。
func (r *Runner) executeDatabaseToolAndSaveResult(ctx context.Context, stepID uuid.UUID, call toolCall) error {
	return pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, r.userID); err != nil {
			return err
		}
		result, err := r.executeDatabaseTool(ctx, queries, call)
		if err != nil {
			return err
		}
		content, sender, visible, err := toolResultContent(call, result)
		if err != nil {
			return err
		}
		if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: r.userID, Role: "tool", Content: content, StepID: &stepID, Sender: sender, Visible: visible}); err != nil {
			return err
		}
		return queries.NotifyUserMessages(ctx, r.userID)
	})
}

// failRun 是失败的统一出口：进程正在退出时不写失败位置，留给重启后从持久状态接着做；否则在一个事务里落库失败收尾，结束本次运行。
// 收尾撤销未发布的占位，给最后一条 assistant 里还没有回执的工具调用各补一条中断回执，避免历史里留下没有回执的调用；
// 未完成的步骤以失败结束，没有更新的输入时记下失败时最后一条消息的序号，之后有新消息才再次运行，避免同一失败反复重试。
func (r *Runner) failRun(ctx context.Context, through int64, cause error) (roundEnd, error) {
	if ctx.Err() != nil {
		return roundStop, nil
	}
	slog.Error("agent run failed", "user_id", r.userID, "error", cause)
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := pgx.BeginFunc(cleanup, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(cleanup, r.userID); err != nil {
			return err
		}
		if err := queries.DiscardGeneratingReply(cleanup, r.userID); err != nil {
			return err
		}
		rows, err := queries.ListAgentContextRows(cleanup, r.userID)
		if err != nil {
			return err
		}
		calls, stepID := pendingToolCalls(rows)
		for _, call := range calls {
			content, _, _, err := toolResultContent(call, "Error: execution was interrupted before this tool call completed.")
			if err != nil {
				return err
			}
			if err := queries.InsertAgentMessage(cleanup, database.InsertAgentMessageParams{UserID: r.userID, Role: "tool", Content: content, StepID: &stepID}); err != nil {
				return err
			}
		}
		errorText := cause.Error()
		if err := queries.FailUnfinishedSteps(cleanup, database.FailUnfinishedStepsParams{UserID: r.userID, Error: &errorText}); err != nil {
			return err
		}
		newer, err := queries.HasNewMainInput(cleanup, database.HasNewMainInputParams{UserID: r.userID, Through: through})
		if err != nil {
			return err
		}
		if !newer {
			if err := queries.MarkMainFailed(cleanup, r.userID); err != nil {
				return err
			}
		}
		return queries.NotifyUserMessages(cleanup, r.userID)
	})
	if err != nil {
		return roundStop, fmt.Errorf("save agent failure: %w", err)
	}
	return roundStop, nil
}
