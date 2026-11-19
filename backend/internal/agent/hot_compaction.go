// 本文件在 Main 完成回答后等待配置的空闲时间，再用原模型和历史前缀生成后台摘要。

package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// hotCompaction 记录目标回答已看见的输入边界，Manager 用它过滤重复广播并中止过时任务。
type hotCompaction struct {
	inputSequence int64
	cancel        context.CancelFunc
}

// scheduleHotCompaction 为本次回答登记可取消的后台任务；进程排空时取消，不占用聊天执行槽位。
func (r *Runner) scheduleHotCompaction(drainCtx context.Context, replyID uuid.UUID, through int64, inputTokens int32) {
	m := r.manager
	finished := time.Now()
	ctx, cancel := context.WithCancel(drainCtx)
	job := &hotCompaction{inputSequence: through, cancel: cancel}
	m.mutex.Lock()
	if previous := m.hotCompactions[r.userID]; previous != nil {
		previous.cancel()
	}
	m.hotCompactions[r.userID] = job
	m.mutex.Unlock()

	m.running.Go(func() {
		defer func() {
			cancel()
			m.mutex.Lock()
			if m.hotCompactions[r.userID] == job {
				delete(m.hotCompactions, r.userID)
			}
			m.mutex.Unlock()
		}()
		if err := r.runHotCompaction(ctx, replyID, inputTokens, finished); err != nil && ctx.Err() == nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("hot compaction failed", "user_id", r.userID, "reply_id", replyID, "error", err)
		}
	})
}

// runHotCompaction 从回答结束计时；只有到点后仍空闲的大上下文才读取历史并发送摘要请求。
func (r *Runner) runHotCompaction(ctx context.Context, replyID uuid.UUID, inputTokens int32, finished time.Time) error {
	q := database.New(r.databasePool)
	settings, err := modelsettings.LoadRuntime(ctx, q)
	if err != nil {
		return err
	}
	if inputTokens < settings.HotCompactAtTokens {
		return nil
	}
	timer := time.NewTimer(time.Until(finished.Add(time.Duration(settings.HotCompactDelaySeconds) * time.Second)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	// 末尾回复同时确定本轮模型和摘要边界。在生成前和落库时 compactContext 会持锁复查它。
	model, err := q.GetLatestMainReplyModel(ctx, database.GetLatestMainReplyModelParams{UserID: r.userID, ReplyID: replyID})
	if err != nil {
		return err
	}
	rows, err := q.ListAgentContextRows(ctx, database.ListAgentContextRowsParams{UserID: r.userID})
	if err != nil {
		return err
	}
	if len(rows) == 0 || rows[len(rows)-1].ID != replyID {
		return nil
	}
	return r.compactContext(ctx, rows, rows[len(rows)-1].Sequence, model, compactPrompt, nil, replyID)
}
