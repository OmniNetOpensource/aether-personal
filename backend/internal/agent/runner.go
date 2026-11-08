// 本文件是单个用户 Main 的 agent loop 主体：每一轮依次走请求前、请求、请求后三个阶段，主体自己只判断还有没有活。
// 三个阶段分别在 before_request.go、request.go、after_request.go，阶段之间只通过 modelRequest 与 chatResponse 交接。

package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 模型单次 HTTP 请求与包含全部重试的调用共用十分钟上限，联网工具另用自己的客户端。
const modelRequestTimeout = 10 * time.Minute

// errModelRetryTimeout 区分模型调用预算耗尽与进程退出等外部中断。
var errModelRetryTimeout = errors.New("model request and retries exceeded 10 minute budget")

// Runner 是一个用户的 Main 在本进程内的一次执行者：有活时由 Manager 创建并独占一条协程，
// 只服务自己这一个用户，消息尾部没有活就结束、由 Manager 注销；进程级依赖按引用共享，不按用户复制。
type Runner struct {
	databasePool *pgxpool.Pool
	settings     config.Config
	modelClient  *http.Client
	toolDeps     tools.Dependencies
	services     *services.Store
	userID       uuid.UUID
	mutex        sync.Mutex
	thinking     *thinkingRequest
}

// thinkingRequest 只在模型还没有产出正文或工具时登记，序号来自实际发送的上下文。
type thinkingRequest struct {
	through int64
	cancel  context.CancelCauseFunc
}

// errNewInput 表示请求被更新的输入取代，应重新读上下文。
var errNewInput = errors.New("superseded by new input")

// newInput 在本用户的模型请求还没有产出正文或工具时，用更新的输入序号取消它，让 Runner 重新读上下文。
func (r *Runner) newInput(sequence int64) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.thinking != nil && sequence > r.thinking.through {
		r.thinking.cancel(errNewInput)
	}
}

// modelRetry 是同一次回复跨轮共享的重试状态：十分钟预算从第一次尝试开始计时，覆盖上下文准备、请求与退避，
// 临时故障最多再试 3 次。回复成功或被新输入取代后清零，下一次回复重新计时。
type modelRetry struct {
	budget  context.Context
	cancel  context.CancelFunc
	retries int
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
	// roundStop 表示失败已收尾或进程正在退出，结束本次运行。
	roundStop
)

// run 是 agent loop：在用户独占的协程里一轮轮执行三个阶段，直到消息尾部没有活、也不需要收尾任务提醒。
// 每一步的结果都先落库，下一轮再从数据库读，所以进程中断后从任何一步都能接着走；失败由请求后阶段落库，
// 只有失败收尾本身或判断有没有活时出错，才返回给 Manager 记录。
func (r *Runner) run(parent context.Context) error {
	queries := database.New(r.databasePool)
	// 运行结束刷新其他连接的消息与思考状态，浏览器连接的生命周期不影响本次执行。
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := queries.NotifyUserMessages(cleanup, r.userID); err != nil {
			slog.Error("notify agent activity failed", "user_id", r.userID, "error", err)
		}
	}()
	// 空唤醒（例如客户端重发同一条消息）直接结束：不进入循环，也就不会在模型没运行时发收尾任务提醒。
	hasWork, err := queries.HasMainWork(parent, r.userID)
	if err != nil {
		return fmt.Errorf("check Main work: %w", err)
	}
	if !hasWork {
		return nil
	}
	// through 是失败时停住的边界，由请求前阶段按本轮实际读到的消息更新。
	var through int64
	var retry modelRetry
	defer func() {
		if retry.cancel != nil {
			retry.cancel()
		}
	}()
	taskReminder := false
	// remindedTaskSignature 记录上一次收尾提醒时的未完成任务集合；集合没变就不重复提醒，避免模型始终不更新任务状态时无限续跑。
	var remindedTaskSignature string
	for {
		if retry.budget == nil {
			retry.budget, retry.cancel = context.WithDeadlineCause(parent, time.Now().Add(modelRequestTimeout), errModelRetryTimeout)
		}
		request, stepID, err := r.beforeRequest(parent, retry.budget, &through, taskReminder)
		var replyID uuid.UUID
		var response chatResponse
		retryable := false
		if err == nil {
			replyID, response, retryable, err = r.sendRequest(parent, retry.budget, stepID, request)
		}
		end, err := r.afterRequest(parent, &retry, through, stepID, replyID, response, retryable, err)
		if err != nil {
			return err
		}
		if end == roundStop {
			return nil
		}
		if end == roundRetry {
			continue
		}
		// 本轮完成后看消息尾部；模型已经收尾但仍有进行中或排队的任务时，不直接结束：对同一批未完成任务只提醒一次，请模型核对并更新任务状态。
		hasWork, err := queries.HasMainWork(parent, r.userID)
		if err != nil {
			return fmt.Errorf("check Main work: %w", err)
		}
		taskReminder = false
		if !hasWork {
			signature, err := r.unfinishedTaskSignature(parent)
			if err != nil {
				return fmt.Errorf("check unfinished tasks: %w", err)
			}
			if signature == "" || signature == remindedTaskSignature {
				return nil
			}
			remindedTaskSignature, taskReminder = signature, true
		}
	}
}

// unfinishedTaskSignature 把进行中与排队的任务拼成一个可比较的签名：没有这类任务时返回空串。
// Runner 用它判断收尾提醒是否已经对同一批未完成任务发过，任务集合发生变化后才允许再次提醒。
func (r *Runner) unfinishedTaskSignature(ctx context.Context) (string, error) {
	rows, err := database.New(r.databasePool).ListTasks(ctx, database.ListTasksParams{UserID: r.userID, IncludeClosed: false})
	if err != nil {
		return "", err
	}
	var signature strings.Builder
	for _, row := range rows {
		if row.Task.Status != "in_progress" && row.Task.Status != "queued" {
			continue
		}
		fmt.Fprintf(&signature, "%s:%s;", row.Task.ID, row.Task.Status)
	}
	return signature.String(), nil
}
