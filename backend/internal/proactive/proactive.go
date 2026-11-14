// 本文件负责系统内置的闲置主动触发：定期按看板配置的闲置时长扫描到期用户，投递不可见触发消息并唤醒 Main 检查潜在需要与记忆更新。

package proactive

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scanInterval 是扫描间隔，闲置主动触发的触发精度就在这个量级。
const scanInterval = 5 * time.Second

// Run 每隔 scanInterval 扫一次闲置到期的用户并投递主动触发，直到进程退出。它是主动触发循环的最外层：
// 失败在这里记录，未投递成功的用户在下一次扫描会再试。
func Run(ctx context.Context, pool *pgxpool.Pool, manager *agent.Manager) {
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	for {
		scanProactive(ctx, pool, manager)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// scanProactive 读取当前闲置时长配置并扫描全部到期用户，逐个投递主动触发；失败只记录日志，留待下次扫描重试。
func scanProactive(ctx context.Context, pool *pgxpool.Pool, manager *agent.Manager) {
	queries := database.New(pool)
	// 闲置时长每次扫描现读看板设置，管理员修改后下一次扫描就生效。
	settings, err := modelsettings.LoadRuntime(ctx, queries)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("load reflection settings failed", "error", err)
		}
		return
	}
	due, err := queries.ListReflectionDue(ctx, database.ListReflectionDueParams{IdleMinutes: settings.ReflectionIdleMinutes})
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("scan reflection failed", "error", err)
		}
		return
	}
	for _, userID := range due {
		if err := deliverProactive(ctx, pool, manager, userID, settings.ReflectionIdleMinutes); err != nil && ctx.Err() == nil {
			slog.Error("deliver reflection failed", "user_id", userID, "error", err)
		}
	}
}

// deliverProactive 给一个闲置到期的用户投一次主动触发：在用户消息锁内按同样条件复查，
// 扫描之后又来了新消息、开始了新请求或已被其他实例抢先投递的用户不再重复投递；提交后唤醒该用户的 Main。
func deliverProactive(ctx context.Context, pool *pgxpool.Pool, manager *agent.Manager, userID uuid.UUID, idleMinutes int32) error {
	var sequence int64
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, userID); err != nil {
			return err
		}
		due, err := queries.ListReflectionDue(ctx, database.ListReflectionDueParams{
			UserID:      &userID,
			IdleMinutes: idleMinutes,
		})
		if err != nil || len(due) == 0 {
			return err
		}
		prompt := fmt.Sprintf(agent.ReflectionPrompt, idleMinutes, time.Now().UTC().Format(time.RFC3339))
		content, err := json.Marshal(map[string]string{"role": "user", "name": agent.ReflectionMessageName, "content": prompt})
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
