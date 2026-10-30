// 本文件是 Agent 的执行调度中心：把数据库里“有消息待处理”的 Main 和 Subagent
// 一个个唤醒运行，保证同一个 Agent 同一时间只跑一处，模型回复落库后再推进下一步。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Subagent 单次被唤醒后最多连续运行的时间，超时就标为失败并通知 Main。
const subagentTimeout = 15 * time.Minute

// 模型单次 HTTP 请求与包含全部重试的调用共用十分钟上限，联网工具另用自己的客户端。
const modelRequestTimeout = 10 * time.Minute

// errModelRetryTimeout 区分模型调用预算耗尽与进程退出、执行权丢失等外部中断。
var errModelRetryTimeout = errors.New("model request and retries exceeded 10 minute budget")

// agentAddress 是运行时定位一个 Agent 的地址：只有 userID 是 Main，二者都有是 Subagent。
type agentAddress struct {
	userID     uuid.UUID
	subagentID *uuid.UUID
}

// lockKey 把地址拼成咨询锁和内部登记表共用的字符串键，Main 与 Subagent 不会撞键。
func (a agentAddress) lockKey() string {
	if a.subagentID != nil {
		return "subagent:" + a.subagentID.String()
	}
	return "main:" + a.userID.String()
}

// Runner 是进程内的执行调度器：它只持有连接池、配置和“谁正在跑”的临时登记，
// 真正的待办队列始终在数据库里，所以进程重启后靠重新扫描就能恢复。
type Runner struct {
	databasePool *pgxpool.Pool
	settings     config.Config
	modelClient  *http.Client
	webTools     webTools
	services     *services.Store
	wakeSignal   chan struct{}
	activeSlots  chan struct{}
	mutex        sync.Mutex
	active       map[string]bool
}

// NewRunner 组装调度器：模型与联网工具分别使用十分钟和两分钟超时的 HTTP 客户端，
// 并按连接池大小预留并发名额，避免 Agent 把普通请求的连接占满。
func NewRunner(pool *pgxpool.Pool, settings config.Config, serviceSettings *services.Store) *Runner {
	return &Runner{
		databasePool: pool, settings: settings, modelClient: &http.Client{Timeout: modelRequestTimeout},
		webTools:   webTools{httpClient: &http.Client{Timeout: 120 * time.Second}, services: serviceSettings},
		services:   serviceSettings,
		wakeSignal: make(chan struct{}, 1), activeSlots: make(chan struct{}, max(1, int(pool.Config().MaxConns)-4)), active: map[string]bool{},
	}
}

// Wake 在有新消息写入后敲一下调度循环，让它立刻重新扫队列；通道已满说明循环已被叫醒，不必排队。
func (r *Runner) Wake(_ uuid.UUID) {
	select {
	case r.wakeSignal <- struct{}{}:
	default:
	}
}

// Start 是常驻调度循环：被 Wake 叫醒或每秒定时醒来时，从数据库扫出待运行的 Agent 并逐个派发。
// 数据库是队列的事实来源，所以这种扫描能补偿进程退出或通知丢失。
func (r *Runner) Start(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	r.Wake(uuid.Nil)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wakeSignal:
		case <-ticker.C:
		}
		addresses, err := database.New(r.databasePool).ListRunnableAgents(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("scan agent queue failed", "error", err)
			}
			continue
		}
		// 派发单个 Agent：本进程已在跑的、或并发名额已满的先跳过，其余登记后开协程执行。
		for _, row := range addresses {
			address := agentAddress{userID: row.UserID, subagentID: row.SubagentID}
			key := address.lockKey()
			r.mutex.Lock()
			if r.active[key] {
				r.mutex.Unlock()
				continue
			}
			select {
			case r.activeSlots <- struct{}{}:
				r.active[key] = true
			default:
				r.mutex.Unlock()
				continue
			}
			r.mutex.Unlock()
			go func() {
				ran := r.run(ctx, address)
				r.mutex.Lock()
				delete(r.active, key)
				r.mutex.Unlock()
				<-r.activeSlots
				if ran {
					r.Wake(address.userID)
				}
			}()
		}
	}
}

// model 按角色选模型：Subagent 用 Subagent 模型，其余（即 Main）用 Main 模型。
func (r *Runner) model(address agentAddress) string {
	if address.subagentID != nil {
		return r.settings.SubagentModel
	}
	return r.settings.MainModel
}

// run 是单个 Agent 的一次完整运行：独占一条连接、抢到该 Agent 的咨询锁才继续，
// 返回值表示“这次确实处理过工作”，调度循环据此决定要不要马上再唤醒一次。
func (r *Runner) run(parent context.Context, address agentAddress) bool {
	connection, err := r.databasePool.Acquire(parent)
	if err != nil {
		return false
	}
	defer connection.Release()
	queries := database.New(connection)
	locked, err := queries.TryLockRuntime(parent, address.lockKey())
	if err != nil || !locked {
		return false
	}
	keys := []string{address.lockKey()}
	// 收尾时按取得顺序的逆序释放全部咨询锁，并通知前端有新消息；释放失败就直接关连接，靠数据库会话结束兜底。
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for index := len(keys) - 1; index >= 0; index-- {
			unlocked, err := queries.UnlockRuntime(cleanup, keys[index])
			if err != nil || !unlocked {
				connection.Conn().Close(cleanup)
				return
			}
		}
		if err := queries.NotifyUserMessages(cleanup, address.userID); err != nil {
			slog.Error("notify agent activity failed", "user_id", address.userID, "error", err)
		}
	}()
	// 先清理上次进程退出留下的未完成记录，避免它们把本次运行误判成还在进行中。
	if err := queries.InterruptModelCalls(parent, database.InterruptModelCallsParams{UserID: address.userID, SubagentID: address.subagentID}); err != nil {
		slog.Error("recover model call journal failed", "error", err)
		return false
	}
	if err := queries.RecoverCompactSteps(parent, database.RecoverCompactStepsParams{UserID: address.userID, SubagentID: address.subagentID}); err != nil {
		slog.Error("recover compact steps failed", "error", err)
		return false
	}
	// 没有待读消息就直接结束，让调度循环把名额让给其他 Agent。
	hasWork, err := queries.HasAgentExecutionWork(parent, database.HasAgentExecutionWorkParams{UserID: address.userID, SubagentID: address.subagentID})
	if err != nil {
		slog.Error("check agent execution work failed", "error", err)
		return false
	}
	if !hasWork {
		return true
	}
	ctx := parent
	// Subagent 额外限流：同一用户最多同时跑 3 个（依次抢 3 把槽位锁），并给本次运行加上 15 分钟截止时间。
	if address.subagentID != nil {
		for slot := 0; slot < 3; slot++ {
			key := fmt.Sprintf("subagent-slot:%s:%d", address.userID, slot)
			ok, lockErr := queries.TryLockRuntime(parent, key)
			if lockErr != nil {
				return false
			}
			if ok {
				keys = append(keys, key)
				break
			}
		}
		if len(keys) != 2 {
			return false
		}
		startedAt, err := queries.StartSubagent(parent, database.StartSubagentParams{UserID: address.userID, ID: *address.subagentID})
		if err != nil {
			slog.Error("start subagent failed", "error", err)
			return false
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(parent, startedAt.Add(subagentTimeout))
		defer cancel()
		if ctx.Err() != nil {
			cleanup, cancelCleanup := context.WithTimeout(parent, 5*time.Second)
			defer cancelCleanup()
			if err := r.failAgent(cleanup, connection, address, context.DeadlineExceeded); err != nil {
				slog.Error("save expired subagent failed", "error", err)
			}
			return true
		}
	}
	// 真正干活的主循环在 runLoop；这里只分流它的结果：推迟不算失败，连接已断不重复记错，其余才落库为失败。
	if err := r.runLoop(ctx, connection, address); err != nil {
		if parent.Err() != nil || connection.Conn().IsClosed() {
			return true
		}
		slog.Error("agent run failed", "agent", address.lockKey(), "error", err)
		if errors.Is(err, errMissingDelivery) {
			return true
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.failAgent(cleanup, connection, address, err); err != nil {
			slog.Error("save agent failure failed", "error", err)
		}
	}
	return true
}

// prepareStep 为下一步模型调用做准备，一个事务里完成：优先续用未完成的步骤，
// 把新到的待读消息标为已读，没有步骤且确有新消息时才新建步骤；返回零值表示暂时没活干。
// 同时返回这一步刚领到的待读消息 ID，上下文准备靠它认出刚到的用户消息（见 compact.go 的新消息触发）。
func (r *Runner) prepareStep(ctx context.Context, connection *pgxpool.Conn, address agentAddress) (uuid.UUID, []uuid.UUID, error) {
	var stepID uuid.UUID
	var newMessageIDs []uuid.UUID
	err := pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		var err error
		stepID, err = queries.GetUnfinishedStep(ctx, database.GetUnfinishedStepParams{UserID: address.userID, SubagentID: address.subagentID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			stored, err := queries.GetStepModelReply(ctx, stepID)
			if err != nil {
				return err
			}
			if len(stored) != 0 {
				return nil
			}
		}
		pending, err := queries.ListPendingAgentMessageIDs(ctx, database.ListPendingAgentMessageIDsParams{UserID: address.userID, SubagentID: address.subagentID})
		if err != nil {
			return err
		}
		newMessageIDs = pending
		for _, id := range pending {
			if err := queries.MarkMessageRead(ctx, id); err != nil {
				return err
			}
		}
		if stepID == uuid.Nil {
			if len(pending) == 0 {
				stepID = uuid.Nil
				return nil
			}
			stepID, err = queries.StartStep(ctx, database.StartStepParams{UserID: address.userID, SubagentID: address.subagentID, Model: r.model(address)})
		}
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			return queries.NotifyUserMessages(ctx, address.userID)
		}
		return nil
	})
	return stepID, newMessageIDs, err
}

// runLoop 是“准备步骤 → 调用模型 → 执行工具 → 保存回复”的循环，一轮处理一步，直到没有待处理消息为止。
func (r *Runner) runLoop(ctx context.Context, connection *pgxpool.Conn, address agentAddress) error {
	queries := database.New(connection)
	// 每一轮先领一个步骤：领不到说明消息已处理完，Subagent 回到 idle、Main 直接退出等待下次唤醒。
	for {
		stepID, newMessageIDs, err := r.prepareStep(ctx, connection, address)
		if err != nil {
			return err
		}
		if stepID == uuid.Nil {
			if address.subagentID != nil {
				return queries.SetSubagentStatus(ctx, database.SetSubagentStatusParams{UserID: address.userID, ID: *address.subagentID, Status: "idle"})
			}
			return nil
		}
		stored, err := queries.GetStepModelReply(ctx, stepID)
		if err != nil {
			return err
		}
		// 取本步的模型回复：已有存档就复用（进程中断后恢复用），否则真正调用模型并立刻存档。
		var reply modelReply
		if len(stored) != 0 {
			var saved storedModelReply
			if err := json.Unmarshal(stored, &saved); err != nil {
				return err
			}
			reply = modelReply{response: saved.Response, stepID: stepID, delivery: deliveryState{satisfied: saved.DeliverySatisfied, corrected: saved.DeliveryCorrected}}
		} else {
			reply, err = r.callWithRetries(ctx, connection, address, stepID, newMessageIDs)
			if err != nil {
				return err
			}
			payload, err := json.Marshal(storedModelReply{Response: reply.response, DeliverySatisfied: reply.delivery.satisfied, DeliveryCorrected: reply.delivery.corrected})
			if err != nil {
				return err
			}
			if err := queries.SaveStepModelReply(ctx, database.SaveStepModelReplyParams{StepID: reply.stepID, Payload: payload}); err != nil {
				return err
			}
		}
		var message assistantMessage
		if err := json.Unmarshal(reply.response.Choices[0].Message, &message); err != nil {
			return err
		}
		// 先把助手消息落库，工具调用从此在历史里可见；随后逐个执行工具并立刻保存各自的回执，全部齐了再结束本步。
		if err := r.saveAssistantMessage(ctx, connection, address, reply.stepID, reply.response.Choices[0].Message); err != nil {
			return err
		}
		// 按模型点名的顺序逐个执行工具：已有回执说明中断前已经执行完，跳过；没有的执行后立刻存回执。
		// 事务外的工具在存回执前崩溃会被重跑，按至少执行一次处理；与回执同一事务的工具不会重复生效。
		for _, call := range message.ToolCalls {
			saved, err := queries.HasStepToolResult(ctx, database.HasStepToolResultParams{StepID: &reply.stepID, ToolCallID: call.ID})
			if err != nil {
				return err
			}
			if saved {
				continue
			}
			// 登记为事务外的工具在 whileRequest 里执行，期间持续检查执行锁连接还活着。
			if tool, ok := toolByName(call.Function.Name); ok && tool.outsideTransaction {
				result, err := whileRequest(ctx, connection, func(requestContext context.Context) (string, error) {
					return tool.execute(requestContext, r, nil, address, call.Function.Arguments)
				})
				if ctx.Err() != nil || connection.Conn().IsClosed() {
					return errors.Join(ctx.Err(), err)
				}
				if err != nil {
					result = "Error: " + err.Error()
				}
				if err := r.saveToolResult(ctx, connection, address, reply.stepID, call, result); err != nil {
					return err
				}
				continue
			}
			// 其余工具与回执在同一事务里提交。未登记的名字也走这里，回执说明该工具不可用。
			if err := r.executeDatabaseToolAndSaveResult(ctx, connection, address, reply.stepID, call); err != nil {
				return err
			}
		}
		// 全部工具都有回执后结束本步，再唤醒调度进入下一轮。
		if err := r.finishReply(ctx, connection, address, reply.stepID, reply.response, message, reply.delivery); err != nil {
			return err
		}
		r.Wake(address.userID)
	}
}

// modelResult 是一次模型请求的原始结果，retryable 标记这次失败是否值得重试。
type modelResult struct {
	response  chatResponse
	retryable bool
}

// modelReply 是归一化后的模型回复：带上它属于哪个步骤，以及（仅 Subagent）这次回复是否已把结果发给 Main。
type modelReply struct {
	response chatResponse
	stepID   uuid.UUID
	delivery deliveryState
}

// storedModelReply 是 modelReply 存进数据库时的形状，进程中断重启后靠它原样恢复，不重复调用模型。
type storedModelReply struct {
	Response          chatResponse `json:"response"`
	DeliverySatisfied bool         `json:"delivery_satisfied"`
	DeliveryCorrected bool         `json:"delivery_corrected"`
}

// callWithRetries 调用模型并处理重试：把这一步刚领到的消息透传给上下文准备（用于新消息压缩触发），
// 上下文准备、请求和退避共用十分钟预算，可重试的失败最多再试 3 次，
// 每次重试都新开一个步骤记录，旧步骤以失败收尾，方便事后按步骤排查。
func (r *Runner) callWithRetries(ctx context.Context, connection *pgxpool.Conn, address agentAddress, stepID uuid.UUID, newMessageIDs []uuid.UUID) (modelReply, error) {
	retryDeadline := time.Now().Add(modelRequestTimeout)
	retryContext, cancel := context.WithDeadlineCause(ctx, retryDeadline, errModelRetryTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		prepared, messages, err := r.prepareContext(retryContext, connection, address, newMessageIDs)
		if err != nil {
			return modelReply{}, err
		}
		// 执行锁仍由外层上下文守护；预算到期后等请求完成日志收尾，再结束步骤，避免两边抢着落库。
		result, callErr := whileRequest(ctx, connection, func(requestContext context.Context) (modelResult, error) {
			modelContext, cancel := context.WithDeadlineCause(requestContext, retryDeadline, errModelRetryTimeout)
			defer cancel()
			response, retryable, err := r.requestModel(modelContext, prepared, database.StartModelCallParams{StepID: stepID, Purpose: "agent"})
			return modelResult{response: response, retryable: retryable}, err
		})
		if callErr == nil {
			return modelReply{response: result.response, stepID: stepID, delivery: inspectDelivery(messages, address)}, nil
		}
		if ctx.Err() != nil || connection.Conn().IsClosed() {
			return modelReply{}, callErr
		}
		if retryContext.Err() != nil {
			callErr = fmt.Errorf("%w: %w", context.Cause(retryContext), callErr)
		}
		canRetry := result.retryable && attempt < 3 && retryContext.Err() == nil
		errorText := callErr.Error()
		err = pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
			queries := database.New(tx)
			if err := queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, Error: &errorText}); err != nil {
				return err
			}
			if canRetry {
				var err error
				stepID, err = queries.StartStep(ctx, database.StartStepParams{UserID: address.userID, SubagentID: address.subagentID, Model: r.model(address)})
				return err
			}
			return nil
		})
		if err != nil {
			return modelReply{}, err
		}
		slog.Error("model request failed", "agent", address.lockKey(), "attempt", attempt+1, "error", callErr)
		if !canRetry {
			return modelReply{}, callErr
		}
		select {
		case <-retryContext.Done():
			return modelReply{}, context.Cause(retryContext)
		case <-time.After(time.Second << attempt):
		}
		stepID, newMessageIDs, err = r.prepareStep(retryContext, connection, address)
		if err != nil {
			return modelReply{}, err
		}
	}
}

// whileRequest 在等待一次外部请求（模型或联网工具）期间看住执行锁连接：
// HTTP 请求进行时这条连接是空闲的，靠每 5 秒 ping 一次确认它还活着，连接一断就取消请求，避免失去执行权后还继续干活。
func whileRequest[T any](ctx context.Context, connection *pgxpool.Conn, request func(context.Context) (T, error)) (T, error) {
	requestContext, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		value T
		err   error
	}
	completed := make(chan result, 1)
	go func() { value, err := request(requestContext); completed <- result{value, err} }()
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case value := <-completed:
			return value.value, value.err
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-heartbeat.C:
			pingContext, cancelPing := context.WithTimeout(ctx, 2*time.Second)
			err := connection.Ping(pingContext)
			cancelPing()
			if err != nil {
				closeContext, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
				connection.Conn().Close(closeContext)
				cancelClose()
				var zero T
				return zero, fmt.Errorf("agent lock connection lost: %w", err)
			}
		}
	}
}

// saveAssistantMessage 在执行任何工具之前先保存这一步的助手消息，工具调用从此成为历史的一部分；
// 中断恢复时同一步已有助手消息就跳过，避免重复插入。
func (r *Runner) saveAssistantMessage(ctx context.Context, connection *pgxpool.Conn, address agentAddress, stepID uuid.UUID, content json.RawMessage) error {
	return pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, address.userID); err != nil {
			return err
		}
		saved, err := queries.HasStepAssistantMessage(ctx, &stepID)
		if err != nil {
			return err
		}
		if saved {
			return nil
		}
		if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: address.userID, SubagentID: address.subagentID, Role: "assistant", Content: content, StepID: &stepID}); err != nil {
			return err
		}
		return queries.NotifyUserMessages(ctx, address.userID)
	})
}

// saveToolResult 保存单个工具的回执，让中断恢复时能按回执判断这个工具已经执行过。
func (r *Runner) saveToolResult(ctx context.Context, connection *pgxpool.Conn, address agentAddress, stepID uuid.UUID, call toolCall, result string) error {
	content, err := json.Marshal(map[string]string{"role": "tool", "tool_call_id": call.ID, "content": result})
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, address.userID); err != nil {
			return err
		}
		if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: address.userID, SubagentID: address.subagentID, Role: "tool", Content: content, StepID: &stepID}); err != nil {
			return err
		}
		return queries.NotifyUserMessages(ctx, address.userID)
	})
}

// executeDatabaseToolAndSaveResult 在一个事务里执行单个数据库工具并保存它的回执：
// 工具的写入与回执要么一起提交、要么一起回滚，所以恢复时看到有回执就代表工具已经生效，不会重复执行。
func (r *Runner) executeDatabaseToolAndSaveResult(ctx context.Context, connection *pgxpool.Conn, address agentAddress, stepID uuid.UUID, call toolCall) error {
	return pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, address.userID); err != nil {
			return err
		}
		result, err := r.executeDatabaseTool(ctx, queries, address, call)
		if err != nil {
			return err
		}
		content, err := json.Marshal(map[string]string{"role": "tool", "tool_call_id": call.ID, "content": result})
		if err != nil {
			return err
		}
		if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: address.userID, SubagentID: address.subagentID, Role: "tool", Content: content, StepID: &stepID}); err != nil {
			return err
		}
		return queries.NotifyUserMessages(ctx, address.userID)
	})
}

// finishReply 在全部工具都有回执后结束本步：记下用量；Main 的正文已在落库时成为用户消息，不做交付检查，
// 只检查 Subagent 是否把结果发给了 Main——没发就追加一条纠正提示再跑一轮，已经纠正过仍没发则按失败收尾；有工具调用或需要纠正时顺手创建下一步。
func (r *Runner) finishReply(ctx context.Context, connection *pgxpool.Conn, address agentAddress, stepID uuid.UUID, response chatResponse, message assistantMessage, delivery deliveryState) error {
	missingDelivery := address.subagentID != nil && len(message.ToolCalls) == 0 && !delivery.satisfied
	terminalFailure := missingDelivery && delivery.corrected
	err := pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, address.userID); err != nil {
			return err
		}
		var deliveryError *string
		if missingDelivery {
			text := errMissingDelivery.Error()
			deliveryError = &text
		}
		if err := queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens, Cost: response.Usage.Cost, Error: deliveryError}); err != nil {
			return err
		}
		if missingDelivery && !terminalFailure {
			correction, err := json.Marshal(map[string]string{"role": "system", "content": deliveryCorrection})
			if err != nil {
				return err
			}
			if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: address.userID, SubagentID: address.subagentID, Role: "system", Content: correction, StepID: &stepID}); err != nil {
				return err
			}
		}
		if len(message.ToolCalls) > 0 || (missingDelivery && !terminalFailure) {
			if _, err := queries.StartStep(ctx, database.StartStepParams{UserID: address.userID, SubagentID: address.subagentID, Model: r.model(address)}); err != nil {
				return err
			}
		} else if terminalFailure && address.subagentID != nil {
			if err := failSubagent(ctx, queries, address, errMissingDelivery); err != nil {
				return err
			}
		} else if address.subagentID != nil {
			if err := queries.SetSubagentStatus(ctx, database.SetSubagentStatusParams{UserID: address.userID, ID: *address.subagentID, Status: "idle"}); err != nil {
				return err
			}
		}
		return queries.NotifyUserMessages(ctx, address.userID)
	})
	if err != nil {
		return err
	}
	// 终止状态和失败通知与真实响应一起提交，调用方不再重复处理。
	if terminalFailure {
		return errMissingDelivery
	}
	return nil
}

// failAgent 把一次运行的失败落库：未完成的步骤全部以失败收尾；助手消息已保存时，先给还没有回执的
// 工具调用补一条中断回执，避免历史里留下没有回执的工具调用；Subagent 还要把排队消息标为已读并通知 Main。
func (r *Runner) failAgent(ctx context.Context, connection *pgxpool.Conn, address agentAddress, cause error) error {
	return pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		errorText := cause.Error()
		if err := queries.LockUserMessages(ctx, address.userID); err != nil {
			return err
		}
		stepID, err := queries.GetUnfinishedStep(ctx, database.GetUnfinishedStepParams{UserID: address.userID, SubagentID: address.subagentID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			stored, err := queries.GetStepModelReply(ctx, stepID)
			if err != nil {
				return err
			}
			assistantSaved, err := queries.HasStepAssistantMessage(ctx, &stepID)
			if err != nil {
				return err
			}
			if assistantSaved && len(stored) != 0 {
				var storedReply storedModelReply
				if err := json.Unmarshal(stored, &storedReply); err != nil {
					return err
				}
				if len(storedReply.Response.Choices) != 0 {
					var interruptedMessage assistantMessage
					if err := json.Unmarshal(storedReply.Response.Choices[0].Message, &interruptedMessage); err != nil {
						return err
					}
					for _, call := range interruptedMessage.ToolCalls {
						hasResult, err := queries.HasStepToolResult(ctx, database.HasStepToolResultParams{StepID: &stepID, ToolCallID: call.ID})
						if err != nil {
							return err
						}
						if hasResult {
							continue
						}
						content, err := json.Marshal(map[string]string{"role": "tool", "tool_call_id": call.ID, "content": "Error: execution was interrupted before this tool call completed."})
						if err != nil {
							return err
						}
						if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: address.userID, SubagentID: address.subagentID, Role: "tool", Content: content, StepID: &stepID}); err != nil {
							return err
						}
					}
				}
			}
		}
		if err := queries.FailUnfinishedSteps(ctx, database.FailUnfinishedStepsParams{UserID: address.userID, SubagentID: address.subagentID, Error: &errorText}); err != nil {
			return err
		}
		if address.subagentID != nil {
			if err := queries.LockUserMessages(ctx, address.userID); err != nil {
				return err
			}
			pending, err := queries.ListPendingAgentMessageIDs(ctx, database.ListPendingAgentMessageIDsParams{UserID: address.userID, SubagentID: address.subagentID})
			if err != nil {
				return err
			}
			for _, id := range pending {
				if err := queries.MarkMessageRead(ctx, id); err != nil {
					return err
				}
			}
			if err := failSubagent(ctx, queries, address, cause); err != nil {
				return err
			}
		}
		return queries.NotifyUserMessages(ctx, address.userID)
	})
}

// failSubagent 结束一个 Subagent：状态置为 failed，并以系统身份给 Main 发一条说明停在哪、为什么停的消息。
func failSubagent(ctx context.Context, queries *database.Queries, address agentAddress, cause error) error {
	if err := queries.SetSubagentStatus(ctx, database.SetSubagentStatusParams{UserID: address.userID, ID: *address.subagentID, Status: "failed"}); err != nil {
		return err
	}
	return deliverText(ctx, queries, agentAddress{userID: address.userID}, "system", address.subagentID, fmt.Sprintf("Subagent %s stopped: %s", address.subagentID, cause))
}
