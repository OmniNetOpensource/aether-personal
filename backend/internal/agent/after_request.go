// 本文件是 agent loop 的请求后阶段：处理本轮的结局。成功就保存回复并执行其中的工具（Subagent 无工具收尾时原子完成并向 Main 投递结果）；
// 被新输入取代或遇到临时故障就安排下一轮；其余失败在这里统一收尾。

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

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/providers"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// afterRequest 是请求后阶段的入口：err 是本轮在请求前或请求阶段遇到的错误，为空表示拿到了完整回复。
// 返回值告诉循环主体下一步：本轮完成、原样重来一轮，或结束本次运行；只有失败收尾本身出错才返回错误。
func (r *Runner) afterRequest(ctx context.Context, retry *modelRetry, through int64, stepID, replyID uuid.UUID, response chatResponse, retryable bool, err error) (roundEnd, error) {
	if err == nil {
		retry.reset()
		var message assistantMessage
		_ = json.Unmarshal(response.Choices[0].Message, &message)
		// Subagent 在没有后续工具调用时即完成本职工作：原子保存最终正文并向 Main 投递 subagent_event。
		if r.subagentID != nil && len(message.ToolCalls) == 0 {
			if err := r.completeSubagentAndAnnounce(ctx, stepID, replyID, response, AssistantTextToUser(response.Choices[0].Message)); err != nil {
				return r.failRun(ctx, through, err)
			}
			return roundStop, nil
		}
		if err := r.saveReply(ctx, stepID, replyID, response); err != nil {
			return r.failRun(ctx, through, err)
		}
		if err := r.executeToolCalls(ctx, stepID, message.ToolCalls); err != nil {
			return r.failRun(ctx, through, err)
		}
		return roundDone, nil
	}
	// Subagent 达到 15 分钟上限属于业务超时而非容器停机，需落库 failed 并通知 Main。
	if r.subagentID != nil && errors.Is(context.Cause(ctx), errSubagentRunTimeout) {
		return r.failRun(ctx, through, errSubagentRunTimeout)
	}
	if ctx.Err() != nil || errors.Is(err, errLostLease) {
		return roundStop, nil
	}
	if errors.Is(err, errNewInput) {
		if err := r.abandonAttempt(ctx, stepID, errNewInput.Error()); err != nil {
			return r.failRun(ctx, through, err)
		}
		retry.reset()
		return roundDone, nil
	}
	// 空截断仍记为无效响应，但本轮第一次遇到时可以用现有退避补试一次。
	if errors.Is(err, providers.ErrClaudeEmptyMaxTokens) {
		retryable = !retry.sawClaudeEmptyMaxTokens
		retry.sawClaudeEmptyMaxTokens = true
	}
	if retry.budget.Err() != nil {
		err = fmt.Errorf("%w: %w", context.Cause(retry.budget), err)
	}
	if !retryable || retry.retries >= 3 || retry.budget.Err() != nil {
		return r.failRun(ctx, through, err)
	}
	slog.Error("model request failed", "user_id", r.userID, "subagent_id", r.subagentID, "attempt", retry.retries+1, "error", err)
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

// abandonAttempt 在租约校验下放弃这一次尝试：撤销未发布的占位，以原因结束这一步；下一轮的尝试会重新开一步。
func (r *Runner) abandonAttempt(ctx context.Context, stepID uuid.UUID, reason string) error {
	return pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := r.lockAndVerifyLease(ctx, queries); err != nil {
			return err
		}
		if err := queries.DiscardGeneratingReply(ctx, database.DiscardGeneratingReplyParams{UserID: r.userID, SubagentID: r.subagentID}); err != nil {
			return err
		}
		return queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, Error: &reason})
	})
}

// saveReply 在一个校验租约的事务里完成助手记录与步骤；仅 Main 的非空回复对用户聊天可见并触发实时推送。
func (r *Runner) saveReply(ctx context.Context, stepID, replyID uuid.UUID, response chatResponse) error {
	visible := r.subagentID == nil && AssistantTextToUser(response.Choices[0].Message) != ""
	err := pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := r.lockAndVerifyLease(ctx, queries); err != nil {
			return err
		}
		if err := queries.CompleteMainReply(ctx, database.CompleteMainReplyParams{ID: replyID, Content: response.Choices[0].Message, Visible: visible}); err != nil {
			return err
		}
		return queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens, Cost: response.Usage.Cost})
	})
	if err != nil {
		return err
	}
	if r.subagentID == nil {
		r.notifyChat()
	}
	return nil
}

// executeToolCalls 并行执行独立工具，Main 的电脑操作则在一个协程里按模型原始顺序执行。
func (r *Runner) executeToolCalls(ctx context.Context, stepID uuid.UUID, calls []toolCall) error {
	toolErrors := make([]error, len(calls))
	execute := func(index int) {
		call := calls[index]
		if tool, ok := r.lookupTool(call.Function.Name); ok && tool.OutsideTransaction {
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
		if tool, ok := r.lookupTool(call.Function.Name); ok && tool.Ordered {
			ordered = append(ordered, index)
			continue
		}
		waitGroup.Go(func() { execute(index) })
	}
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
// Main 的 UserVisibleParts 工具（sandbox_send_file 与 forward_subagent）成功时附带展示用的 parts 并标为对聊天可见。
func (r *Runner) toolResultContent(call toolCall, result string) ([]byte, *string, bool, error) {
	var content any = result
	trimmed := strings.TrimSpace(result)
	tool, ok := r.lookupTool(call.Function.Name)
	if r.subagentID == nil && ok && tool.UserVisibleParts && strings.HasPrefix(trimmed, "{") {
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

// saveToolResult 在校验租约后保存单个工具的回执；Subagent 只锁自身记录，不阻塞用户的主聊天写入。
func (r *Runner) saveToolResult(ctx context.Context, stepID uuid.UUID, call toolCall, result string) error {
	content, sender, visible, err := r.toolResultContent(call, result)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := r.lockAndVerifyLease(ctx, queries); err != nil {
			return err
		}
		return queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{
			UserID:     r.userID,
			SubagentID: r.subagentID,
			Role:       "tool",
			Content:    content,
			StepID:     &stepID,
			Sender:     sender,
			Visible:    visible,
		})
	})
	if err != nil {
		return err
	}
	if r.subagentID == nil {
		r.notifyChat()
	}
	return nil
}

// executeDatabaseTool 执行与回执同一事务的工具；未登记或当前角色无权使用的名字返回工具错误，让模型可以改口。
func (r *Runner) executeDatabaseTool(ctx context.Context, queries *database.Queries, call toolCall) (string, error) {
	tool, ok := r.lookupTool(call.Function.Name)
	if !ok {
		return "Error: tool not available: " + call.Function.Name, nil
	}
	return tool.Execute(ctx, r.toolDeps, queries, r.userID, call.Function.Arguments)
}

// executeDatabaseToolAndSaveResult 在一个校验租约的事务里执行单个数据库工具并保存它的回执，提交后发出必要的 Subagent 信号与聊天通知。
func (r *Runner) executeDatabaseToolAndSaveResult(ctx context.Context, stepID uuid.UUID, call toolCall) error {
	var result string
	err := pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := r.lockAndVerifyLease(ctx, queries); err != nil {
			return err
		}
		var err error
		result, err = r.executeDatabaseTool(ctx, queries, call)
		if err != nil {
			return err
		}
		content, sender, visible, err := r.toolResultContent(call, result)
		if err != nil {
			return err
		}
		return queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{
			UserID:     r.userID,
			SubagentID: r.subagentID,
			Role:       "tool",
			Content:    content,
			StepID:     &stepID,
			Sender:     sender,
			Visible:    visible,
		})
	})
	if err != nil {
		return err
	}
	if r.subagentID == nil {
		r.onToolCommitted(call.Function.Name, result)
		r.notifyChat()
	}
	return nil
}

// failRun 是失败的统一出口：Subagent 走 failSubagentAndAnnounce 向 Main 汇报错误；Main 则在没有更新输入时记下失败边界停住重试。
func (r *Runner) failRun(ctx context.Context, through int64, cause error) (roundEnd, error) {
	if r.subagentID != nil {
		if errors.Is(cause, errLostLease) || (ctx.Err() != nil && !errors.Is(context.Cause(ctx), errSubagentRunTimeout)) {
			return roundStop, nil
		}
		return r.failSubagentAndAnnounce(cause)
	}
	if ctx.Err() != nil || errors.Is(cause, errLostLease) {
		return roundStop, nil
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := pgx.BeginFunc(cleanup, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := r.lockAndVerifyLease(cleanup, queries); err != nil {
			return err
		}
		if err := queries.DiscardGeneratingReply(cleanup, database.DiscardGeneratingReplyParams{UserID: r.userID}); err != nil {
			return err
		}
		rows, err := queries.ListAgentContextRows(cleanup, database.ListAgentContextRowsParams{UserID: r.userID})
		if err != nil {
			return err
		}
		calls, stepID := pendingToolCalls(rows)
		for _, call := range calls {
			content, _, _, err := r.toolResultContent(call, "Error: execution was interrupted before this tool call completed.")
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
		return nil
	})
	if errors.Is(err, errLostLease) {
		return roundStop, nil
	}
	slog.Error("agent run failed", "user_id", r.userID, "error", cause)
	if err != nil {
		return roundStop, fmt.Errorf("save agent failure: %w", err)
	}
	r.notifyChat()
	return roundStop, nil
}
