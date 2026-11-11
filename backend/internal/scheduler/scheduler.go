// 本文件是后台调度器：定期扫描到期的定时安排与闲置到期的对话，把保存的上下文或三种后台工作（记忆整理、近期跟进、未知的未知）的指令作为不可见触发消息投进该用户的对话，再唤醒 Main。

package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scanInterval 是扫描间隔，定时安排与闲置后台工作的触发精度就在这个量级。
const scanInterval = 5 * time.Second

// idleWork 描述一种在对话闲置后投给 Main 的后台工作。previousName 是这段对话里必须先投过的前一种工作，
// 空表示没有前一种，所以即使看板把时长设乱，三种工作仍按顺序运行。
type idleWork struct {
	name         string
	previousName string
	prompt       string
	idleMinutes  int32
}

// Run 每隔 scanInterval 扫一次到期安排和到期的闲置后台工作并逐条投递，直到进程退出。它是调度器的最外层：
// 失败在这里记录，没投递成功的安排或后台工作仍然到期，下一次扫描会再试。
func Run(ctx context.Context, pool *pgxpool.Pool, appConfig config.Config, manager *agent.Manager) {
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		queries := database.New(pool)
		due, err := queries.ListDueSchedules(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("scan schedules failed", "error", err)
		}
		for _, schedule := range due {
			if err := deliverSchedule(ctx, pool, manager, schedule.UserID, schedule.ID); err != nil && ctx.Err() == nil {
				slog.Error("deliver schedule failed", "schedule_id", schedule.ID, "error", err)
			}
		}
		// 闲置时长每次扫描现读看板设置，管理员修改后下一次扫描就生效。
		settings, err := modelsettings.Load(ctx, queries, appConfig)
		if err != nil && ctx.Err() == nil {
			slog.Error("load idle work settings failed", "error", err)
		}
		if err == nil {
			works := []idleWork{
				{name: agent.MemoryUpdateMessageName, prompt: agent.MemoryUpdatePrompt, idleMinutes: settings.MemoryUpdateIdleMinutes},
				{name: agent.RecentFollowupMessageName, previousName: agent.MemoryUpdateMessageName, prompt: agent.RecentFollowupPrompt, idleMinutes: settings.RecentFollowupIdleMinutes},
				{name: agent.UnknownUnknownsMessageName, previousName: agent.RecentFollowupMessageName, prompt: agent.UnknownUnknownsPrompt, idleMinutes: settings.UnknownUnknownsIdleMinutes},
			}
			for _, work := range works {
				due, err := queries.ListIdleWorkDue(ctx, database.ListIdleWorkDueParams{WorkName: work.name, PreviousWorkName: work.previousName, IdleMinutes: work.idleMinutes})
				if err != nil && ctx.Err() == nil {
					slog.Error("scan idle work failed", "work", work.name, "error", err)
				}
				for _, userID := range due {
					if err := deliverIdleWork(ctx, pool, manager, userID, work); err != nil && ctx.Err() == nil {
						slog.Error("deliver idle work failed", "work", work.name, "user_id", userID, "error", err)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// deliverSchedule 在一个事务里投递一条到期安排：插入触发消息，再删掉一次性安排或把周期安排推进到下一次。
// 两步同时提交，所以不会出现消息投了、安排却没推进而被下次扫描重复投递；提交后唤醒该用户的 Main。
func deliverSchedule(ctx context.Context, pool *pgxpool.Pool, manager *agent.Manager, userID, scheduleID uuid.UUID) error {
	var sequence int64
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, userID); err != nil {
			return err
		}
		// 扫描之后模型可能已把它改到未来或删除，这时不再投递。
		item, err := queries.LockDueSchedule(ctx, scheduleID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		deliveredAt := time.Now().UTC()
		event, err := json.Marshal(map[string]any{"schedule_id": item.ID, "title": item.Title, "scheduled_for": item.NextRunAt,
			"delivered_at": deliveredAt, "cron": item.Cron, "timezone": item.Timezone, "context": item.Context})
		if err != nil {
			return err
		}
		content, err := json.Marshal(map[string]string{"role": "user", "name": agent.ScheduleEventMessageName, "content": agent.ScheduleEventPrompt + "\n" + string(event)})
		if err != nil {
			return err
		}
		sequence, err = queries.InsertTriggerMessage(ctx, database.InsertTriggerMessageParams{UserID: userID, Content: content})
		if err != nil {
			return err
		}
		// 周期安排从投递时刻往后算下一次，停机期间错过的多次触发因此合并成这一次投递。
		if item.Cron == nil {
			_, err := queries.DeleteSchedule(ctx, database.DeleteScheduleParams{UserID: userID, ID: item.ID})
			return err
		}
		next, err := tools.NextCronTime(*item.Cron, *item.Timezone, deliveredAt)
		if err != nil {
			return err
		}
		return queries.AdvanceSchedule(ctx, database.AdvanceScheduleParams{ID: item.ID, NextRunAt: next})
	})
	if err != nil || sequence == 0 {
		return err
	}
	manager.Wake(ctx, userID, sequence)
	return nil
}

// deliverIdleWork 给一个闲置到期的用户投一次某种后台工作的触发：在用户消息锁内按同样条件复查，
// 扫描之后刚好来了新消息或这种工作已经投过的用户不再投递；提交后唤醒该用户的 Main。
func deliverIdleWork(ctx context.Context, pool *pgxpool.Pool, manager *agent.Manager, userID uuid.UUID, work idleWork) error {
	var sequence int64
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, userID); err != nil {
			return err
		}
		due, err := queries.ListIdleWorkDue(ctx, database.ListIdleWorkDueParams{UserID: &userID, WorkName: work.name, PreviousWorkName: work.previousName, IdleMinutes: work.idleMinutes})
		if err != nil || len(due) == 0 {
			return err
		}
		prompt := fmt.Sprintf(work.prompt, work.idleMinutes, time.Now().UTC().Format(time.RFC3339))
		content, err := json.Marshal(map[string]string{"role": "user", "name": work.name, "content": prompt})
		if err != nil {
			return err
		}
		sequence, err = queries.InsertTriggerMessage(ctx, database.InsertTriggerMessageParams{UserID: userID, Content: content})
		return err
	})
	if err != nil || sequence == 0 {
		return err
	}
	manager.Wake(ctx, userID, sequence)
	return nil
}
