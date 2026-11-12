// 本文件承载后台 Subagent 的运行时辅助：细粒度锁与工具边界、事务提交后的 Redis 唤醒/取消信号，以及完成或失败时向 Main 投递 subagent_event。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	subagentWakeQueueKey          = "agent:subagent_wake_queue"
	subagentCancelChannel         = "agent:subagent_cancel"
	subagentRunTimeout            = 15 * time.Minute
	maxConcurrentSubagentsPerUser = 5
)

// errSubagentRunTimeout 标明 Subagent 从创建起算已满 15 分钟上限，需标为 failed 并向 Main 回报。
var errSubagentRunTimeout = errors.New("subagent exceeded 15 minute execution limit")

// errSubagentCancelled 标明 Subagent 已被 Main 主动取消，Runner 应直接静默退出。
var errSubagentCancelled = errors.New("subagent cancelled by Main")

// acquireSubagentLeaseLua 原子检查目标租约与该用户当前活跃 Subagent 并发数，未达上限时写入租约。
// 返回 1 表示抢占成功，0 表示已被其他实例持有，-1 表示该用户并发已满（留在数据库待前序释放后补位）。
const acquireSubagentLeaseLua = `
if redis.call("EXISTS", ARGV[1]) == 1 then
	return 0
end
local active = 0
for i = 1, #KEYS do
	if redis.call("EXISTS", KEYS[i]) == 1 then
		active = active + 1
	end
end
if active >= tonumber(ARGV[4]) then
	return -1
end
redis.call("SET", ARGV[1], ARGV[2], "PX", ARGV[3])
return 1
`

// subagentLeaseKey 生成单个 Subagent 执行租约的 Redis 键名。
func subagentLeaseKey(subagentID uuid.UUID) string {
	return "agent:subagent_lease:" + subagentID.String()
}

// toolDefinitions 按当前 Runner 身份返回 Main 全量工具或 Subagent 只读工具定义。
func (r *Runner) toolDefinitions() []json.RawMessage {
	if r.subagentID != nil {
		return tools.SubagentDefinitions()
	}
	return tools.Definitions()
}

// lookupTool 按当前 Runner 身份查找允许调用的工具，物理阻止 Subagent 调用越权工具。
func (r *Runner) lookupTool(name string) (tools.Tool, bool) {
	if r.subagentID != nil {
		return tools.SubagentByName(name)
	}
	return tools.ByName(name)
}

// onToolCommitted 在 Main 的数据库工具事务成功提交后发出对应的 Redis 唤醒或取消广播，避免未提交事务提前触发消费。
func (r *Runner) onToolCommitted(name, result string) {
	if r.subagentID != nil || r.manager == nil {
		return
	}
	if name != "spawn_subagent" && name != "cancel_subagent" {
		return
	}
	var receipt tools.SubagentReceipt
	if json.Unmarshal([]byte(result), &receipt) != nil || receipt.SubagentID == uuid.Nil {
		return
	}
	signalCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if name == "spawn_subagent" && receipt.Status == "running" {
		if err := r.manager.enqueueSubagentWake(signalCtx, receipt.SubagentID); err != nil {
			slog.Error("enqueue spawned subagent failed", "subagent_id", receipt.SubagentID, "error", err)
		}
		return
	}
	if name == "cancel_subagent" && receipt.WasRunning {
		r.manager.broadcastSubagentCancel(signalCtx, r.userID, receipt.SubagentID)
	}
}

// completeSubagentAndAnnounce 在单一事务内按固定锁序（先用户消息锁、后 Subagent 锁）保存最终回复、标为 completed，
// 并向 Main 的消息列表投递 subagent_event 触发消息，提交后唤醒 Main。
func (r *Runner) completeSubagentAndAnnounce(ctx context.Context, stepID, replyID uuid.UUID, response chatResponse, outputText string) error {
	var sequence int64
	err := pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, r.userID); err != nil {
			return err
		}
		if err := r.lockAndVerifyLease(ctx, queries); err != nil {
			return err
		}
		if err := queries.CompleteMainReply(ctx, database.CompleteMainReplyParams{
			ID:      replyID,
			Content: response.Choices[0].Message,
			Visible: false,
		}); err != nil {
			return err
		}
		if err := queries.FinishStep(ctx, database.FinishStepParams{
			ID:           stepID,
			InputTokens:  response.Usage.PromptTokens,
			OutputTokens: response.Usage.CompletionTokens,
			Cost:         response.Usage.Cost,
		}); err != nil {
			return err
		}
		subagent, err := queries.GetSubagentForUser(ctx, database.GetSubagentForUserParams{UserID: r.userID, ID: *r.subagentID})
		if err != nil {
			return err
		}
		affected, err := queries.CompleteSubagent(ctx, database.CompleteSubagentParams{
			SubagentID: *r.subagentID,
			Output:     outputText,
		})
		if err != nil {
			return err
		}
		if affected != 1 {
			return errLostLease
		}
		eventPayload, err := json.Marshal(map[string]any{
			"subagent_id": subagent.ID,
			"title":       subagent.Title,
			"status":      "completed",
			"finished_at": time.Now().UTC(),
			"output":      outputText,
		})
		if err != nil {
			return err
		}
		triggerContent, err := json.Marshal(map[string]string{
			"role":    "user",
			"name":    SubagentEventMessageName,
			"content": SubagentEventPrompt + "\n" + string(eventPayload),
		})
		if err != nil {
			return err
		}
		sequence, err = queries.InsertTriggerMessage(ctx, database.InsertTriggerMessageParams{
			UserID:  r.userID,
			Content: triggerContent,
		})
		return err
	})
	if err != nil {
		return err
	}
	if r.manager != nil {
		r.manager.Wake(ctx, r.userID, sequence)
	}
	return nil
}

// failSubagentAndAnnounce 在 Subagent 耗尽重试或达到 15 分钟上限时按固定锁序落库失败原因，并向 Main 投递失败通知。
func (r *Runner) failSubagentAndAnnounce(cause error) (roundEnd, error) {
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var sequence int64
	err := pgx.BeginFunc(cleanup, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(cleanup, r.userID); err != nil {
			return err
		}
		if err := r.lockAndVerifyLease(cleanup, queries); err != nil {
			return err
		}
		if err := queries.DiscardGeneratingReply(cleanup, database.DiscardGeneratingReplyParams{UserID: r.userID, SubagentID: r.subagentID}); err != nil {
			return err
		}
		rows, err := queries.ListAgentContextRows(cleanup, database.ListAgentContextRowsParams{UserID: r.userID, SubagentID: r.subagentID})
		if err != nil {
			return err
		}
		calls, stepID := pendingToolCalls(rows)
		for _, call := range calls {
			content, _, _, err := r.toolResultContent(call, "Error: execution was interrupted before this tool call completed.")
			if err != nil {
				return err
			}
			if err := queries.InsertAgentMessage(cleanup, database.InsertAgentMessageParams{
				UserID:     r.userID,
				SubagentID: r.subagentID,
				Role:       "tool",
				Content:    content,
				StepID:     &stepID,
			}); err != nil {
				return err
			}
		}
		errorText := cause.Error()
		if err := queries.FailUnfinishedSteps(cleanup, database.FailUnfinishedStepsParams{
			UserID:     r.userID,
			SubagentID: r.subagentID,
			Error:      &errorText,
		}); err != nil {
			return err
		}
		subagent, err := queries.GetSubagentForUser(cleanup, database.GetSubagentForUserParams{UserID: r.userID, ID: *r.subagentID})
		if err != nil {
			return err
		}
		affected, err := queries.FailSubagent(cleanup, database.FailSubagentParams{
			SubagentID: *r.subagentID,
			Error:      errorText,
		})
		if err != nil {
			return err
		}
		if affected != 1 {
			return errLostLease
		}
		eventPayload, err := json.Marshal(map[string]any{
			"subagent_id": subagent.ID,
			"title":       subagent.Title,
			"status":      "failed",
			"finished_at": time.Now().UTC(),
			"error":       errorText,
		})
		if err != nil {
			return err
		}
		triggerContent, err := json.Marshal(map[string]string{
			"role":    "user",
			"name":    SubagentEventMessageName,
			"content": SubagentEventPrompt + "\n" + string(eventPayload),
		})
		if err != nil {
			return err
		}
		sequence, err = queries.InsertTriggerMessage(cleanup, database.InsertTriggerMessageParams{
			UserID:  r.userID,
			Content: triggerContent,
		})
		return err
	})
	if errors.Is(err, errLostLease) {
		return roundStop, nil
	}
	slog.Error("subagent run failed", "user_id", r.userID, "subagent_id", *r.subagentID, "error", cause)
	if err != nil {
		return roundStop, fmt.Errorf("save subagent failure: %w", err)
	}
	if r.manager != nil {
		r.manager.Wake(context.Background(), r.userID, sequence)
	}
	return roundStop, nil
}
