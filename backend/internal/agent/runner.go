// 本文件执行单个用户的 Main；Manager 保证进程内串行，模型和工具等待期间不占用数据库连接。
// Runner 根据消息尾部决定下一步：补跑没有回执的工具，或再调用一次模型；模型收尾后若还有进行中或排队的任务，用一轮任务提醒续跑，仍没有工作才结束并注销。

package agent

import (
	"context"
	"encoding/json"
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
	"github.com/jackc/pgx/v5"
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

// run 在用户独占的协程里恢复中断记录，再按持久待办循环执行；临时数据库错误返回 Manager 退避重试。
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
	// 先收尾上次进程退出留下的记录：进行中的模型调用记为中断，未结束的步骤以中断收尾。
	// 它们只供看板查看，不影响下面按消息尾部接着运行。
	if err := queries.InterruptModelCalls(parent, r.userID); err != nil {
		return fmt.Errorf("recover model call journal: %w", err)
	}
	// 单进程内没有其他 Runner，清掉上次中断且未向用户发布的占位。
	if err := queries.DiscardGeneratingReply(parent, r.userID); err != nil {
		return fmt.Errorf("recover incomplete reply: %w", err)
	}
	interrupted := "interrupted before the step finished"
	if err := queries.FailUnfinishedSteps(parent, database.FailUnfinishedStepsParams{UserID: r.userID, Error: &interrupted}); err != nil {
		return fmt.Errorf("recover unfinished steps: %w", err)
	}
	// 消息尾部已经回复完毕、或停在上次失败处，就直接结束，让调度循环把名额让给其他 Agent。
	hasWork, err := queries.HasMainWork(parent, r.userID)
	if err != nil {
		return fmt.Errorf("check Main work: %w", err)
	}
	if !hasWork {
		return nil
	}
	// 每一步的结果都先落库，下一轮再从数据库读，所以进程中断后从任何一步都能接着走。
	var through int64
	var loopErr error
	// remindedTaskSignature 记录上一次收尾提醒时的未完成任务集合；集合没变就不重复提醒，避免模型始终不更新任务状态时无限续跑。
	var remindedTaskSignature string
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
			if loopErr = r.executeToolCalls(parent, stepID, calls); loopErr != nil {
				break
			}
			continue
		}
		// 消息尾部已经回复完毕就停下；任务队列本身不再单独算作待办。
		hasWork, err := queries.HasMainWork(parent, r.userID)
		if err != nil {
			loopErr = err
			break
		}
		// 模型已经收尾但仍有进行中或排队的任务时，不直接结束：对同一批未完成任务只提醒一次，请模型核对并更新任务状态后再结束。
		taskReminder := false
		if !hasWork {
			signature, err := r.unfinishedTaskSignature(parent)
			if err != nil {
				loopErr = err
				break
			}
			if signature == "" || signature == remindedTaskSignature {
				break
			}
			remindedTaskSignature = signature
			taskReminder = true
		}
		if loopErr = r.reply(parent, &through, taskReminder); loopErr != nil {
			break
		}
	}
	if loopErr != nil {
		if parent.Err() != nil {
			return nil
		}
		// 临时数据库故障保留原持久状态，交给 Manager 延迟重试本用户，不写入业务失败位置。
		if temporaryDatabaseError(loopErr) {
			return loopErr
		}
		slog.Error("agent run failed", "user_id", r.userID, "error", loopErr)
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.failAgent(cleanup, through, loopErr); err != nil {
			return fmt.Errorf("save agent failure: %w", err)
		}
	}
	return nil
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

// executeToolCalls 并行执行尚无回执的工具，每个调用仅在保存结果时使用连接池。
// 数据库工具与回执同事务提交；外部调用中断后可能重跑，已保存的回执不会重复执行。
func (r *Runner) executeToolCalls(ctx context.Context, stepID uuid.UUID, calls []toolCall) error {
	// 全部工具同时开工，每个协程只写自己下标的结果槽，等待结束后按调用顺序取第一个硬错误。
	toolErrors := make([]error, len(calls))
	var waitGroup sync.WaitGroup
	for index, call := range calls {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			// 联网工具在事务外执行，失败转成工具错误回执存下来，让模型下一轮自己修正；批次被取消时不存回执，整批按中断处理。
			if tool, ok := tools.ByName(call.Function.Name); ok && tool.OutsideTransaction {
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
			// 其余工具与回执在同一事务里提交。未登记的名字也走这里，回执说明该工具不可用。
			toolErrors[index] = r.executeDatabaseToolAndSaveResult(ctx, stepID, call)
		}()
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

// reply 调用模型并把回复存成 assistant 消息。每次尝试是一个步骤：上下文准备（含可能的压缩）、请求和退避共用十分钟预算，
// 传输等临时故障最多再试 3 次；失败尝试以错误收尾。每次尝试都重新组装上下文，期间新到的消息会一并带上。
// taskReminder 为 true 时这一轮由收尾提醒触发，组装上下文时在末尾带上任务提醒。
func (r *Runner) reply(ctx context.Context, through *int64, taskReminder bool) error {
	queries := database.New(r.databasePool)
	retryDeadline := time.Now().Add(modelRequestTimeout)
	retryContext, cancel := context.WithDeadlineCause(ctx, retryDeadline, errModelRetryTimeout)
	defer cancel()
	transientRetries := 0
	for attempt := 0; ; attempt++ {
		stepID, err := queries.StartStep(ctx, database.StartStepParams{UserID: r.userID, Model: r.settings.MainModel})
		if err != nil {
			return err
		}
		prepared, err := r.prepareContext(retryContext, taskReminder)
		if err != nil {
			return err
		}
		*through = prepared.InputSequence
		retryable, callErr := r.streamMainReply(ctx, retryContext, stepID, prepared)
		if callErr == nil {
			return nil
		}
		// 同一用户只有当前 Runner，失败重试前先撤销未发布的占位。
		if ctx.Err() == nil {
			if err := queries.DiscardGeneratingReply(ctx, r.userID); err != nil {
				return err
			}
		}
		if errors.Is(callErr, errNewInput) {
			reason := errNewInput.Error()
			return queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, Error: &reason})
		}
		if ctx.Err() != nil {
			return callErr
		}
		if retryContext.Err() != nil {
			callErr = fmt.Errorf("%w: %w", context.Cause(retryContext), callErr)
		}
		errorText := callErr.Error()
		if err := queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, Error: &errorText}); err != nil {
			return err
		}
		slog.Error("model request failed", "user_id", r.userID, "attempt", attempt+1, "error", callErr)
		if retryContext.Err() != nil {
			return callErr
		}
		if !retryable || transientRetries >= 3 {
			return callErr
		}
		delay := time.Second << transientRetries
		transientRetries++
		select {
		case <-retryContext.Done():
			return context.Cause(retryContext)
		case <-time.After(delay):
		}
	}
}

// streamMainReply 只允许新的输入取消尚未占位的请求。占位与输入插入使用同一把事务锁确定先后。
func (r *Runner) streamMainReply(ctx, budget context.Context, stepID uuid.UUID, prepared modelRequest) (bool, error) {
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
	newer, err := database.New(r.databasePool).HasNewMainInput(ctx, input)
	if err != nil {
		return false, err
	}
	if newer {
		return false, errNewInput
	}

	var replyID uuid.UUID
	reserve := func() error {
		stopListening()
		return pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
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
	response, retryable, err := r.sendModelRequest(modelCtx, prepared,
		database.StartModelCallParams{StepID: stepID, Purpose: "agent"}, reserve)
	if errors.Is(context.Cause(modelCtx), errNewInput) {
		return false, errNewInput
	}
	if err != nil {
		return retryable, err
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
	return false, r.saveReply(ctx, stepID, replyID, response)
}

// saveReply 在一个事务里完成助手记录与步骤：正文独立成为用户回复，有正文才对用户可见。
// 工具调用从此成为历史的一部分，下一轮按消息尾部看到它们没有回执，就去执行。
func (r *Runner) saveReply(ctx context.Context, stepID, replyID uuid.UUID, response chatResponse) error {
	return pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, r.userID); err != nil {
			return err
		}
		if err := queries.CompleteMainReply(ctx, database.CompleteMainReplyParams{ID: replyID, Content: response.Choices[0].Message, Visible: AssistantTextToUser(response.Choices[0].Message) != ""}); err != nil {
			return err
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
	if !ok {
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
func (r *Runner) failAgent(ctx context.Context, through int64, cause error) error {
	return pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
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
