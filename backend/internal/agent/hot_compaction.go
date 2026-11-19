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

// scheduleHotCompaction 为本次回答登记后台热压缩任务；等待阶段可被新输入或进程排空取消，开始调用模型后不再被新输入打断。
func (r *Runner) scheduleHotCompaction(drainCtx context.Context, replyID uuid.UUID, through int64, inputTokens int32) {
	m := r.manager
	finished := time.Now()
	waitCtx, cancelWait := context.WithCancel(drainCtx)
	job := &hotCompaction{inputSequence: through, cancel: cancelWait}
	m.mutex.Lock()
	if previous := m.hotCompactions[r.userID]; previous != nil {
		previous.cancel()
	}
	m.hotCompactions[r.userID] = job
	m.mutex.Unlock()

	stopWaiting := func() {
		cancelWait()
		m.mutex.Lock()
		if m.hotCompactions[r.userID] == job {
			delete(m.hotCompactions, r.userID)
		}
		m.mutex.Unlock()
	}

	m.running.Go(func() {
		defer stopWaiting()
		if err := r.runHotCompaction(waitCtx, drainCtx, stopWaiting, replyID, inputTokens, finished); err != nil && drainCtx.Err() == nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("hot compaction failed", "user_id", r.userID, "reply_id", replyID, "error", err)
		}
	})
}

// runHotCompaction 从回答结束计时；等待期遇新输入直接取消，到点开始生成后不再被新输入中断。
func (r *Runner) runHotCompaction(waitCtx, drainCtx context.Context, stopWaiting func(), replyID uuid.UUID, inputTokens int32, finished time.Time) error {
	q := database.New(r.databasePool)
	settings, err := modelsettings.LoadRuntime(waitCtx, q)
	if err != nil {
		if waitCtx.Err() != nil {
			return nil
		}
		return err
	}
	if inputTokens < settings.HotCompactAtTokens {
		return nil
	}
	timer := time.NewTimer(time.Until(finished.Add(time.Duration(settings.HotCompactDelaySeconds) * time.Second)))
	defer timer.Stop()
	select {
	case <-waitCtx.Done():
		return nil
	case <-timer.C:
	}
	// 等待期结束，先注销新输入取消句柄，此后已发出的压缩请求不再因用户新消息中断。
	stopWaiting()

	// 末尾回复同时确定本轮模型和摘要边界。compactContext 开步前会持锁复查它仍是末尾。
	model, err := q.GetLatestMainReplyModel(drainCtx, database.GetLatestMainReplyModelParams{UserID: r.userID, ReplyID: replyID})
	if err != nil {
		return err
	}
	rows, err := q.ListAgentContextRows(drainCtx, database.ListAgentContextRowsParams{UserID: r.userID})
	if err != nil {
		return err
	}
	if len(rows) == 0 || rows[len(rows)-1].ID != replyID {
		return nil
	}
	return r.compactContext(drainCtx, rows, rows[len(rows)-1].Sequence, model, compactPrompt, nil, replyID)
}
