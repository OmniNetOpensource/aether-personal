// 本文件是 Main 与后台 Subagent 共用的 agent loop 主体：每一轮依次走请求前、请求、请求后三个阶段，主体自己只判断还有没有活。
// 三个阶段分别在 before_request.go、request.go、after_request.go，阶段之间只通过 modelRequest 与 chatResponse 交接。

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// 模型单次 HTTP 请求与包含全部重试的调用共用十分钟上限，联网工具另用自己的客户端。
const modelRequestTimeout = 10 * time.Minute

// errModelRetryTimeout 区分模型调用预算耗尽与进程退出等外部中断。
var errModelRetryTimeout = errors.New("model request and retries exceeded 10 minute budget")

// errLostLease 表示当前 Agent 的执行租约已被新实例接管或已被取消，本 Runner 应立即静默停止且不记为运行失败。
var errLostLease = errors.New("agent execution lease was superseded")

// Runner 是单个 Main（subagentID == nil）或单个后台 Subagent（subagentID != nil）在本进程内的一次执行者：
// 有活时由 Manager 抢占对应租约后创建并独占一条协程，每次写库校验各自的 leaseEpoch 防止跨实例覆盖。
type Runner struct {
	manager      *Manager
	databasePool *pgxpool.Pool
	redisClient  *redis.Client
	modelClient  *http.Client
	toolDeps     tools.Dependencies
	services     *services.Store
	userID       uuid.UUID
	subagentID   *uuid.UUID
	startedAt    time.Time
	leaseEpoch   int64
	mutex        sync.Mutex
	thinking     *thinkingRequest
}

// lockAndVerifyLease 按身份获取对应的事务锁并核对执行代数：Main 锁用户消息并核对 main_lease_epoch，
// Subagent 锁自身记录并核对仍处于 running 且 lease_epoch 未变。
func (r *Runner) lockAndVerifyLease(ctx context.Context, queries *database.Queries) error {
	if r.subagentID != nil {
		if err := queries.LockSubagent(ctx, *r.subagentID); err != nil {
			return err
		}
		state, err := queries.GetSubagentLeaseState(ctx, *r.subagentID)
		if err != nil {
			return err
		}
		if state.Status != "running" || state.LeaseEpoch != r.leaseEpoch {
			return errLostLease
		}
		return nil
	}
	if err := queries.LockUserMessages(ctx, r.userID); err != nil {
		return err
	}
	epoch, err := queries.GetUserLeaseEpoch(ctx, r.userID)
	if err != nil {
		return err
	}
	if epoch != r.leaseEpoch {
		return errLostLease
	}
	return nil
}

// hasWork 判断当前 Runner 对应的 Main 或 Subagent 是否还有待处理工作。
func (r *Runner) hasWork(ctx context.Context) (bool, error) {
	queries := database.New(r.databasePool)
	if r.subagentID != nil {
		return queries.HasSubagentWork(ctx, *r.subagentID)
	}
	return queries.HasMainWork(ctx, r.userID)
}

// notifyChat 使用独立短超时向 Redis 广播聊天状态更新，唤醒持有该用户 WebSocket 的实例查表推送。
func (r *Runner) notifyChat() {
	notifyCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := NotifyChat(notifyCtx, r.redisClient, r.userID); err != nil {
		slog.Error("notify agent activity failed", "user_id", r.userID, "error", err)
	}
}

// thinkingRequest 只在 Main 模型还没有产出正文或工具时登记，序号来自实际发送的上下文。
type thinkingRequest struct {
	through int64
	cancel  context.CancelCauseFunc
}

// errNewInput 表示请求被更新的输入取代，应重新读上下文。
var errNewInput = errors.New("superseded by new input")

// newInput 在本用户 Main 的模型请求还没有产出正文或工具时，用更新的输入序号取消它，让 Runner 重新读上下文。
func (r *Runner) newInput(sequence int64) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.thinking != nil && sequence > r.thinking.through {
		r.thinking.cancel(errNewInput)
	}
}

// modelRetry 是同一次回复跨轮共享的重试状态：十分钟预算从第一次尝试开始计时，覆盖上下文准备、请求与退避，
// 共可再试 3 次，其中 ikun Claude 空截断最多补试一次。回复成功或被新输入取代后清零。
type modelRetry struct {
	budget                  context.Context
	cancel                  context.CancelFunc
	retries                 int
	sawClaudeEmptyMaxTokens bool
}

// reset 结束这次回复的预算，下一轮开始时重新计时。
func (retry *modelRetry) reset() {
	retry.cancel()
	*retry = modelRetry{}
}

// roundEnd 是请求后阶段对本轮结局的处理结果，循环主体据此决定下一步。
type roundEnd int

const (
	// roundDone 表示本轮已完成（回复已保存，或被新输入取代），按消息尾部判断还有没有活。
	roundDone roundEnd = iota
	// roundRetry 表示临时故障的退避已结束，原样再走一轮，不重新判断有没有活。
	roundRetry
	// roundStop 表示已完成交付、失败已收尾或进程正在退出，结束本次运行。
	roundStop
)

// run 是 agent loop：在独占协程里一轮轮执行三个阶段，直到当前 Agent 没有活。
// 每一步的结果都先落库，下一轮再从数据库读，所以进程中断后从任何一步都能接着走。
// 启动与结束时的聊天通知由 Manager 调度层在租约生效后与释放后统一触发。
func (r *Runner) run(parent context.Context) error {
	hasWork, err := r.hasWork(parent)
	if err != nil {
		return fmt.Errorf("check agent work: %w", err)
	}
	if !hasWork {
		return nil
	}
	runCtx := parent
	var cancelTimeout context.CancelFunc
	if r.subagentID != nil {
		runCtx, cancelTimeout = context.WithDeadlineCause(parent, r.startedAt.Add(subagentRunTimeout), errSubagentRunTimeout)
		defer cancelTimeout()
	}
	// through 是 Main 失败时停住的边界，由请求前阶段按本轮实际读到的消息更新。
	var through int64
	var retry modelRetry
	defer func() {
		if retry.cancel != nil {
			retry.cancel()
		}
	}()
	for {
		if retry.budget == nil {
			retry.budget, retry.cancel = context.WithDeadlineCause(runCtx, time.Now().Add(modelRequestTimeout), errModelRetryTimeout)
		}
		request, stepID, err := r.beforeRequest(runCtx, retry.budget, &through)
		var replyID uuid.UUID
		var response chatResponse
		retryable := false
		if err == nil {
			replyID, response, retryable, err = r.sendRequest(runCtx, retry.budget, stepID, request)
		}
		end, err := r.afterRequest(runCtx, &retry, through, stepID, replyID, response, retryable, err)
		if err != nil {
			return err
		}
		if end == roundStop {
			return nil
		}
		if end == roundRetry {
			continue
		}
		hasWork, err := r.hasWork(runCtx)
		if err != nil {
			return fmt.Errorf("check agent work: %w", err)
		}
		if !hasWork {
			return nil
		}
	}
}
