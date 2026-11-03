// 本文件是 Agent 的执行调度中心：Manager 只认数据库通知转发来的唤醒，为每个有活的用户创建一个 Runner；
// Runner 只看自己消息表尾部决定下一步——补跑没有回执的工具、调用模型，或者已经回复完毕就结束并注销。

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

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 模型单次 HTTP 请求与包含全部重试的调用共用十分钟上限，联网工具另用自己的客户端。
const modelRequestTimeout = 10 * time.Minute

// errModelRetryTimeout 区分模型调用预算耗尽与进程退出、执行权丢失等外部中断。
var errModelRetryTimeout = errors.New("model request and retries exceeded 10 minute budget")

// runtimeLockKey 把用户 ID 拼成该用户 Main 咨询锁的字符串键。
func runtimeLockKey(userID uuid.UUID) string {
	return "main:" + userID.String()
}

// Manager 是进程内唯一的 Agent 调度器：只持有进程级共享依赖和“哪个用户正在跑”的登记，
// 要不要运行只由消息表尾部决定（见 main_work 视图）；唤醒只来自 PostgreSQL 通知，重启后遗留的工作等下一次通知触发扫描时恢复。
type Manager struct {
	databasePool *pgxpool.Pool
	settings     config.Config
	modelClient  *http.Client
	toolDeps     tools.Dependencies
	services     *services.Store
	wakeSignal   chan struct{}
	activeSlots  chan struct{}
	mutex        sync.Mutex
	runners      map[uuid.UUID]*Runner
}

// NewManager 组装调度器：模型与联网工具分别使用十分钟和两分钟超时的 HTTP 客户端，
// 并按连接池大小预留并发名额，避免 Agent 把普通请求的连接占满。
func NewManager(pool *pgxpool.Pool, settings config.Config, serviceSettings *services.Store) *Manager {
	return &Manager{
		databasePool: pool, settings: settings, modelClient: &http.Client{Timeout: modelRequestTimeout},
		toolDeps:   tools.Dependencies{DatabasePool: pool, Services: serviceSettings, HTTPClient: &http.Client{Timeout: 120 * time.Second}},
		services:   serviceSettings,
		wakeSignal: make(chan struct{}, 1), activeSlots: make(chan struct{}, max(1, int(pool.Config().MaxConns)-4)), runners: map[uuid.UUID]*Runner{},
	}
}

// Wake 只由 PostgreSQL 通知的监听转发调用，敲一下调度循环让它重新扫描；通道已满说明循环已被叫醒，不必排队。
func (m *Manager) Wake(_ uuid.UUID) {
	select {
	case m.wakeSignal <- struct{}{}:
	default:
	}
}

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

// newRunner 为一个用户组装 Runner：连接池、模型客户端等共享依赖直接引用 Manager 持有的那一份。
func (m *Manager) newRunner(userID uuid.UUID) *Runner {
	return &Runner{
		databasePool: m.databasePool, settings: m.settings, modelClient: m.modelClient,
		toolDeps: m.toolDeps, services: m.services, userID: userID,
	}
}

// thinkingRequest 只在模型还没有产出正文或工具时登记，序号来自实际发送的上下文。
type thinkingRequest struct {
	through int64
	cancel  context.CancelCauseFunc
}

// errNewInput 表示请求被更新的输入取代，应重新读上下文。
var errNewInput = errors.New("superseded by new input")

// NewInput 把新输入通知给正拥有这个用户的 Runner（若有），同时唤醒调度器处理空闲用户。
func (m *Manager) NewInput(userID uuid.UUID, sequence int64) {
	m.mutex.Lock()
	runner := m.runners[userID]
	m.mutex.Unlock()
	if runner != nil {
		runner.newInput(sequence)
	}
	m.Wake(userID)
}

// newInput 在本用户的模型请求还没有产出正文或工具时，用更新的输入序号取消它，让 Runner 重新读上下文。
func (r *Runner) newInput(sequence int64) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if r.thinking != nil && sequence > r.thinking.through {
		r.thinking.cancel(errNewInput)
	}
}

// Start 是常驻调度循环：只在 PostgreSQL 通知转发来的 Wake 时醒来，从数据库扫出消息尾部需要 Main 处理的用户，
// 为每个还没有 Runner 的用户创建一个并开协程执行，Runner 结束时注销并释放并发名额。
// 没有启动扫描与周期扫描：通知丢失时，遗留工作等下一次通知触发的全量扫描顺带恢复，不保证恢复时间。
func (m *Manager) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wakeSignal:
		}
		userIDs, err := database.New(m.databasePool).ListRunnableAgents(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("scan agent queue failed", "error", err)
			}
			continue
		}
		// 派发单个 Main：本进程已有 Runner 的、或并发名额已满的先跳过，其余创建 Runner 登记后开协程执行。
		for _, userID := range userIDs {
			m.mutex.Lock()
			if _, ok := m.runners[userID]; ok {
				m.mutex.Unlock()
				continue
			}
			select {
			case m.activeSlots <- struct{}{}:
			default:
				m.mutex.Unlock()
				continue
			}
			runner := m.newRunner(userID)
			m.runners[userID] = runner
			m.mutex.Unlock()
			go func() {
				// 运行已经结束；先从登记表移除本 Runner，再归还它占用的并发名额。
				defer func() {
					m.mutex.Lock()
					if m.runners[userID] == runner {
						delete(m.runners, userID)
					}
					m.mutex.Unlock()
					<-m.activeSlots
				}()
				runner.run(ctx)
			}()
		}
	}
}

// run 是单个 Main 的一次完整运行：独占连接并抢到咨询锁后，先收尾上次崩溃，再按消息尾部循环执行缺回执的工具或调用模型，直到已经回复完毕。
// 循环中的失败只在末尾分流，进程退出或连接已断不记失败，其余才落库。
func (r *Runner) run(parent context.Context) {
	connection, err := r.databasePool.Acquire(parent)
	if err != nil {
		return
	}
	defer connection.Release()
	queries := database.New(connection)
	locked, err := queries.TryLockRuntime(parent, runtimeLockKey(r.userID))
	if err != nil || !locked {
		return
	}
	// 收尾时释放咨询锁，并通知前端刷新消息与思考状态；释放失败就直接关连接，靠数据库会话结束兜底。
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		unlocked, err := queries.UnlockRuntime(cleanup, runtimeLockKey(r.userID))
		if err != nil || !unlocked {
			connection.Conn().Close(cleanup)
			return
		}
		if err := queries.NotifyUserMessages(cleanup, r.userID); err != nil {
			slog.Error("notify agent activity failed", "user_id", r.userID, "error", err)
		}
	}()
	// 先收尾上次进程退出留下的记录：进行中的模型调用记为中断，未结束的步骤以中断收尾。
	// 它们只供看板查看，不影响下面按消息尾部接着运行。
	if err := queries.InterruptModelCalls(parent, r.userID); err != nil {
		slog.Error("recover model call journal failed", "error", err)
		return
	}
	// 执行锁保证上次进程已经退出，清掉其未完成且未向用户发布的占位。
	if err := queries.DiscardGeneratingReply(parent, r.userID); err != nil {
		slog.Error("recover incomplete reply", "error", err)
		return
	}
	interrupted := "interrupted before the step finished"
	if err := queries.FailUnfinishedSteps(parent, database.FailUnfinishedStepsParams{UserID: r.userID, Error: &interrupted}); err != nil {
		slog.Error("recover unfinished steps failed", "error", err)
		return
	}
	// 消息尾部已经回复完毕、或停在上次失败处，就直接结束，让调度循环把名额让给其他 Agent。
	hasWork, err := queries.HasMainWork(parent, r.userID)
	if err != nil {
		slog.Error("check Main work failed", "error", err)
		return
	}
	if !hasWork {
		return
	}
	// 每一步的结果都先落库，下一轮再从数据库读，所以进程中断后从任何一步都能接着走。
	var through int64
	var loopErr error
	for {
		rows, err := queries.ListAgentContextRows(parent, r.userID)
		if err != nil {
			loopErr = err
			break
		}
		// 失败只停住本次读到的工作；工具批次以发起它的助手消息为界。
		for _, row := range rows {
			through = max(through, row.Sequence)
		}
		if calls, stepID := pendingToolCalls(rows); len(calls) > 0 {
			for _, row := range rows {
				if row.StepID != nil && *row.StepID == stepID && parseContextMessage(row.Content).Role == "assistant" {
					through = row.Sequence
					break
				}
			}
			if loopErr = r.executeToolCalls(parent, connection, stepID, calls); loopErr != nil {
				break
			}
			continue
		}
		if !needsReply(rows) {
			break
		}
		if loopErr = r.reply(parent, connection, &through); loopErr != nil {
			break
		}
	}
	if loopErr != nil {
		if parent.Err() != nil || connection.Conn().IsClosed() {
			return
		}
		slog.Error("agent run failed", "agent", runtimeLockKey(r.userID), "error", loopErr)
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.failAgent(cleanup, connection, through, loopErr); err != nil {
			slog.Error("save agent failure failed", "error", err)
		}
	}
}

// executeToolCalls 并行执行同一条模型回复里还没有回执的全部工具，不设并发上限，每个调用起一个协程。
// 执行锁连接由调用方独占，不能被多个协程同时使用，所以每个工具都改走连接池：
// 数据库工具在各自从池中取连接的事务里执行并连回执一起提交，联网工具执行完立刻用池中连接单独存回执；
// 批次执行期间另有看守协程定期 ping 锁连接，连接失效就取消整批，等看守退出后调用方才继续用锁连接。
// 事务外的工具在存回执前崩溃会被重跑，按至少执行一次处理；与回执同一事务的工具不会重复生效。
func (r *Runner) executeToolCalls(ctx context.Context, connection *pgxpool.Conn, stepID uuid.UUID, calls []toolCall) error {
	batchContext, cancelBatch := context.WithCancel(ctx)
	defer cancelBatch()
	// 看守协程在整批工具执行期间看住执行锁连接：连接一断就关掉它并取消整批，避免失去执行权后还继续干活。
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		heartbeat := time.NewTicker(5 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-batchContext.Done():
				return
			case <-heartbeat.C:
				pingContext, cancelPing := context.WithTimeout(batchContext, 2*time.Second)
				err := connection.Ping(pingContext)
				cancelPing()
				if err == nil {
					continue
				}
				if batchContext.Err() != nil {
					return
				}
				closeContext, cancelClose := context.WithTimeout(context.Background(), 2*time.Second)
				connection.Conn().Close(closeContext)
				cancelClose()
				cancelBatch()
				return
			}
		}
	}()
	// 全部工具同时开工，每个协程只写自己下标的结果槽，等待结束后按调用顺序取第一个硬错误。
	toolErrors := make([]error, len(calls))
	var waitGroup sync.WaitGroup
	for index, call := range calls {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			// 联网工具在事务外执行，失败转成工具错误回执存下来，让模型下一轮自己修正；批次被取消时不存回执，整批按中断处理。
			if tool, ok := tools.ByName(call.Function.Name); ok && tool.OutsideTransaction {
				result, err := tool.Execute(batchContext, r.toolDeps, nil, r.userID, call.Function.Arguments)
				if err != nil {
					if batchContext.Err() != nil {
						toolErrors[index] = err
						return
					}
					result = "Error: " + err.Error()
				}
				toolErrors[index] = r.saveToolResult(batchContext, stepID, call, result)
				return
			}
			// 其余工具与回执在同一事务里提交。未登记的名字也走这里，回执说明该工具不可用。
			toolErrors[index] = r.executeDatabaseToolAndSaveResult(batchContext, stepID, call)
		}()
	}
	waitGroup.Wait()
	cancelBatch()
	<-watcherDone
	// 上下文取消或锁连接失效属于整批中断：已存的回执保留，未存的由下次运行按消息尾部重跑，或由失败收尾补中断回执。
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if connection.Conn().IsClosed() {
		return errors.New("agent lock connection lost during tool execution")
	}
	for _, err := range toolErrors {
		if err != nil {
			return err
		}
	}
	return nil
}

// modelResult 是一次模型请求的原始结果，retryable 标记这次失败是否值得重试。
type modelResult struct {
	response  chatResponse
	retryable bool
}

// reply 调用模型并把回复存成 assistant 消息。每次尝试是一个步骤：上下文准备（含可能的压缩）、请求和退避共用十分钟预算，
// 传输等临时故障最多再试 3 次，空回复单独最多补试一次；失败尝试以错误收尾。每次尝试都重新组装上下文，期间新到的消息会一并带上。
func (r *Runner) reply(ctx context.Context, connection *pgxpool.Conn, through *int64) error {
	queries := database.New(connection)
	retryDeadline := time.Now().Add(modelRequestTimeout)
	retryContext, cancel := context.WithDeadlineCause(ctx, retryDeadline, errModelRetryTimeout)
	defer cancel()
	transientRetries, emptyRetries := 0, 0
	for attempt := 0; ; attempt++ {
		stepID, err := queries.StartStep(ctx, database.StartStepParams{UserID: r.userID, Model: r.settings.MainModel})
		if err != nil {
			return err
		}
		prepared, err := r.prepareContext(retryContext, connection)
		if err != nil {
			return err
		}
		*through = prepared.InputSequence
		retryable, callErr := r.streamMainReply(ctx, retryContext, connection, stepID, prepared)
		if callErr == nil {
			return nil
		}
		// 同一用户的占位仅归当前锁持有者，失败重试前先撤销未发布的行。
		if ctx.Err() == nil && !connection.Conn().IsClosed() {
			if err := queries.DiscardGeneratingReply(ctx, r.userID); err != nil {
				return err
			}
		}
		if errors.Is(callErr, errNewInput) {
			reason := errNewInput.Error()
			return queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, Error: &reason})
		}
		if ctx.Err() != nil || connection.Conn().IsClosed() {
			return callErr
		}
		if retryContext.Err() != nil {
			callErr = fmt.Errorf("%w: %w", context.Cause(retryContext), callErr)
		}
		errorText := callErr.Error()
		if err := queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, Error: &errorText}); err != nil {
			return err
		}
		slog.Error("model request failed", "agent", runtimeLockKey(r.userID), "attempt", attempt+1, "error", callErr)
		if retryContext.Err() != nil {
			return callErr
		}
		// 空回复与网络重试分开计数，避免把模型反复沉默扩大成四次付费生成。
		delay := time.Second
		if errors.Is(callErr, errEmptyReply) {
			if emptyRetries >= 1 {
				return callErr
			}
			emptyRetries++
		} else {
			if !retryable || transientRetries >= 3 {
				return callErr
			}
			delay <<= transientRetries
			transientRetries++
		}
		select {
		case <-retryContext.Done():
			return context.Cause(retryContext)
		case <-time.After(delay):
		}
	}
}

// streamMainReply 只允许新的输入取消尚未占位的请求。占位与输入插入使用同一把事务锁确定先后。
func (r *Runner) streamMainReply(ctx, budget context.Context, conn *pgxpool.Conn, stepID uuid.UUID, prepared modelRequest) (bool, error) {
	modelCtx, cancel := context.WithCancelCause(budget)
	defer cancel(nil)
	r.mutex.Lock()
	r.thinking = &thinkingRequest{prepared.InputSequence, cancel}
	r.mutex.Unlock()
	stopListening := func() {
		r.mutex.Lock()
		r.thinking = nil
		r.mutex.Unlock()
	}
	defer stopListening()

	// 登记之后补查组装期间的输入，避免早于登记到达的通知留下陈旧请求。
	input := database.HasNewMainInputParams{UserID: r.userID, Through: prepared.InputSequence}
	newer, err := database.New(conn).HasNewMainInput(ctx, input)
	if err != nil {
		return false, err
	}
	if newer {
		return false, errNewInput
	}

	var replyID uuid.UUID
	reserve := func() error {
		stopListening()
		return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			q := database.New(tx)
			if err := q.LockUserMessages(ctx, r.userID); err != nil {
				return err
			}
			newer, err := q.HasNewMainInput(ctx, input)
			if err != nil {
				return err
			}
			if newer {
				cancel(errNewInput)
				return errNewInput
			}
			replyID, err = q.ReserveMainReply(ctx, database.ReserveMainReplyParams{UserID: r.userID, StepID: &stepID})
			return err
		})
	}
	result, err := whileRequest(modelCtx, conn, reserve, func(callCtx context.Context, onOutput func() error) (modelResult, error) {
		response, retryable, err := r.sendModelRequest(callCtx, prepared, database.StartModelCallParams{StepID: stepID, Purpose: "agent"}, onOutput)
		return modelResult{response, retryable}, err
	})
	if errors.Is(context.Cause(modelCtx), errNewInput) {
		return false, errNewInput
	}
	if err != nil {
		return result.retryable, err
	}
	// 合法的沉默回复也在同一写入锁下与新输入交接。
	if replyID == uuid.Nil {
		if err := reserve(); err != nil {
			if errors.Is(context.Cause(modelCtx), errNewInput) {
				return false, errNewInput
			}
			return false, err
		}
	}
	return false, r.saveReply(ctx, conn, stepID, replyID, result.response)
}

// whileRequest 在等待一次模型请求期间看住执行锁连接（并行工具批次有自己的看守，见 executeToolCalls）：
// HTTP 请求进行时这条连接是空闲的，靠每 5 秒 ping 一次确认它还活着，连接一断就取消请求，避免失去执行权后还继续干活。
func whileRequest[T any](ctx context.Context, connection *pgxpool.Conn, onOutput func() error, request func(context.Context, func() error) (T, error)) (T, error) {
	requestContext, cancel := context.WithCancel(ctx)
	type result struct {
		value T
		err   error
	}
	completed := make(chan result, 1)
	// 首个输出交给执行锁连接的持有者写库，避免与保活同时使用同一条连接。
	firstOutput := make(chan chan error)
	var observe func() error
	if onOutput != nil {
		observe = func() error {
			ack := make(chan error, 1)
			select {
			case firstOutput <- ack:
			case <-requestContext.Done():
				return context.Cause(requestContext)
			}
			select {
			case err := <-ack:
				return err
			case <-requestContext.Done():
				return context.Cause(requestContext)
			}
		}
	}
	go func() { value, err := request(requestContext, observe); completed <- result{value, err} }()
	// 所有退出路径都等模型日志收尾再释放执行锁。
	stopped := false
	defer func() {
		cancel()
		if !stopped {
			<-completed
		}
	}()
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case ack := <-firstOutput:
			ack <- onOutput()
		case value := <-completed:
			stopped = true
			return value.value, value.err
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-heartbeat.C:
			// 用户输入取消的是模型 HTTP 请求，避免此时取消 ping 导致 pgx 关闭执行锁连接。
			pingContext, cancelPing := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
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

// saveReply 在一个事务里保存模型回复：写入 assistant 消息并给这一步记上用量。
// 工具调用从此成为历史的一部分，下一轮按消息尾部看到它们没有回执，就去执行。
func (r *Runner) saveReply(ctx context.Context, connection *pgxpool.Conn, stepID, replyID uuid.UUID, response chatResponse) error {
	return pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, r.userID); err != nil {
			return err
		}
		n, err := queries.CompleteMainReply(ctx, database.CompleteMainReplyParams{ID: replyID, Content: response.Choices[0].Message})
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("generating reply disappeared")
		}
		if err := queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, InputTokens: response.Usage.PromptTokens, OutputTokens: response.Usage.CompletionTokens, Cost: response.Usage.Cost}); err != nil {
			return err
		}
		return queries.NotifyUserMessages(ctx, r.userID)
	})
}

// toolResultContent 把一个工具调用的结果写成原生 tool 消息，靠 tool_call_id 与调用对应。
func toolResultContent(call toolCall, result string) ([]byte, error) {
	return json.Marshal(map[string]string{"role": "tool", "tool_call_id": call.ID, "content": result})
}

// saveToolResult 保存单个工具的回执，让中断恢复时能按回执判断这个工具已经执行过；
// 工具并行执行时从连接池取连接保存，同一用户的插入由 LockUserMessages 在数据库内串行排队。
func (r *Runner) saveToolResult(ctx context.Context, stepID uuid.UUID, call toolCall, result string) error {
	content, err := toolResultContent(call, result)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, r.userID); err != nil {
			return err
		}
		if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: r.userID, Role: "tool", Content: content, StepID: &stepID}); err != nil {
			return err
		}
		return queries.NotifyUserMessages(ctx, r.userID)
	})
}

// executeDatabaseTool 执行与回执同一事务的工具。未登记的名字返回工具错误，让模型可以改口。
func (r *Runner) executeDatabaseTool(ctx context.Context, queries *database.Queries, call toolCall) (string, error) {
	tool, ok := tools.ByName(call.Function.Name)
	if !ok || tool.OutsideTransaction {
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
		content, err := toolResultContent(call, result)
		if err != nil {
			return err
		}
		if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: r.userID, Role: "tool", Content: content, StepID: &stepID}); err != nil {
			return err
		}
		return queries.NotifyUserMessages(ctx, r.userID)
	})
}

// failAgent 把一次运行的失败落库：最后一条 assistant 里还没有回执的工具调用各补一条中断回执，避免历史里留下没有回执的调用；
// 未完成的步骤以失败收尾，并记下失败时最后一条消息的序号，之后有新消息才再次运行，避免同一失败反复重试。
func (r *Runner) failAgent(ctx context.Context, connection *pgxpool.Conn, through int64, cause error) error {
	return pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, r.userID); err != nil {
			return err
		}
		rows, err := queries.ListAgentContextRows(ctx, r.userID)
		if err != nil {
			return err
		}
		calls, stepID := pendingToolCalls(rows)
		for _, call := range calls {
			content, err := toolResultContent(call, "Error: execution was interrupted before this tool call completed.")
			if err != nil {
				return err
			}
			if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: r.userID, Role: "tool", Content: content, StepID: &stepID}); err != nil {
				return err
			}
		}
		errorText := cause.Error()
		if err := queries.FailUnfinishedSteps(ctx, database.FailUnfinishedStepsParams{UserID: r.userID, Error: &errorText}); err != nil {
			return err
		}
		newer, err := queries.HasNewMainInput(ctx, database.HasNewMainInputParams{UserID: r.userID, Through: through})
		if err != nil {
			return err
		}
		if !newer {
			if err := queries.MarkMainFailed(ctx, r.userID); err != nil {
				return err
			}
		}
		return queries.NotifyUserMessages(ctx, r.userID)
	})
}
