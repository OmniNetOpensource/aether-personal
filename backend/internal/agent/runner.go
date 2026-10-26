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

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/sandbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const subagentTimeout = 15 * time.Minute

var errSandboxDeferred = errors.New("sandbox is controlled by another agent")

type agentAddress struct {
	userID     uuid.UUID
	subagentID *uuid.UUID
}

func (a agentAddress) lockKey() string {
	if a.subagentID != nil {
		return "subagent:" + a.subagentID.String()
	}
	return "main:" + a.userID.String()
}

type Runner struct {
	databasePool  *pgxpool.Pool
	settings      config.Config
	httpClient    *http.Client
	webTools      webTools
	sandbox       *sandbox.Client
	wakeSignal    chan struct{}
	recapWake     chan struct{}
	activeSlots   chan struct{}
	mutex         sync.Mutex
	active        map[string]bool
	modelContexts map[string]int64
}

func NewRunner(pool *pgxpool.Pool, settings config.Config) *Runner {
	client := &http.Client{Timeout: 120 * time.Second}
	runner := &Runner{
		recapWake:    make(chan struct{}, 1),
		databasePool: pool, settings: settings, httpClient: client,
		webTools:   webTools{httpClient: client, serpAPIKey: settings.SERPAPIKey, parallelAPIKey: settings.ParallelAPIKey, everyInfraAPIKey: settings.EveryInfraAPIKey},
		wakeSignal: make(chan struct{}, 1), activeSlots: make(chan struct{}, max(1, int(pool.Config().MaxConns)-4)), active: map[string]bool{}, modelContexts: map[string]int64{},
	}
	if settings.SandboxURL != "" {
		runner.sandbox = sandbox.NewClient(settings.SandboxURL, settings.SandboxAPIKey, client)
	}
	return runner
}

func (r *Runner) Wake(_ uuid.UUID) {
	r.wakeRecap()
	select {
	case r.wakeSignal <- struct{}{}:
	default:
	}
}

// 数据库是队列的事实来源，扫描补偿进程退出或通知丢失。
func (r *Runner) Start(ctx context.Context) {
	if r.sandbox != nil {
		go r.pauseIdleSandboxes(ctx)
	}
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

func (r *Runner) model(address agentAddress) string {
	if address.subagentID != nil {
		return r.settings.SubagentModel
	}
	return r.settings.MainModel
}

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
		if _, err := queries.SyncRecapState(cleanup, address.userID); err != nil {
			slog.Warn("sync finished agent recap failed", "user_id", address.userID, "error", err)
		}
		if err := queries.NotifyUserMessages(cleanup, address.userID); err != nil {
			slog.Error("notify agent activity failed", "user_id", address.userID, "error", err)
		}
	}()
	if err := queries.InterruptModelCalls(parent, database.InterruptModelCallsParams{UserID: address.userID, SubagentID: address.subagentID}); err != nil {
		slog.Error("recover model call journal failed", "error", err)
		return false
	}
	if err := queries.RecoverCompactSteps(parent, database.RecoverCompactStepsParams{UserID: address.userID, SubagentID: address.subagentID}); err != nil {
		slog.Error("recover compact steps failed", "error", err)
		return false
	}
	hasWork, err := queries.HasAgentExecutionWork(parent, database.HasAgentExecutionWorkParams{UserID: address.userID, SubagentID: address.subagentID})
	if err != nil {
		slog.Error("check agent execution work failed", "error", err)
		return false
	}
	if !hasWork {
		return true
	}
	ctx := parent
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
	if err := r.runLoop(ctx, connection, address); err != nil {
		if errors.Is(err, errSandboxDeferred) {
			return false
		}
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

func (r *Runner) prepareStep(ctx context.Context, connection *pgxpool.Conn, address agentAddress) (uuid.UUID, error) {
	var stepID uuid.UUID
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
	return stepID, err
}

func (r *Runner) runLoop(ctx context.Context, connection *pgxpool.Conn, address agentAddress) error {
	queries := database.New(connection)
	sandboxLocked := false
	var sandboxOwnedAt time.Time
	unlockSandbox := func() {
		if !sandboxLocked {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		unlocked, err := queries.UnlockRuntime(cleanup, "sandbox:"+address.userID.String())
		if err != nil || !unlocked {
			connection.Conn().Close(cleanup)
		}
		sandboxLocked = false
	}
	defer unlockSandbox()
	for {
		stepID, err := r.prepareStep(ctx, connection, address)
		if err != nil {
			return err
		}
		if stepID == uuid.Nil {
			if address.subagentID != nil {
				return queries.SetSubagentStatus(ctx, database.SetSubagentStatusParams{UserID: address.userID, ID: *address.subagentID, Status: "idle"})
			}
			return nil
		}
		if sandboxLocked && time.Since(sandboxOwnedAt) >= 15*time.Minute {
			unlockSandbox()
			if err := queries.DeferSandboxStep(ctx, stepID); err != nil {
				return err
			}
			return errSandboxDeferred
		}
		stored, err := queries.GetStepModelReply(ctx, stepID)
		if err != nil {
			return err
		}
		var reply modelReply
		if len(stored) != 0 {
			var saved storedModelReply
			if err := json.Unmarshal(stored, &saved); err != nil {
				return err
			}
			reply = modelReply{response: saved.Response, stepID: stepID, delivery: deliveryState{satisfied: saved.DeliverySatisfied, corrected: saved.DeliveryCorrected}}
		} else {
			reply, err = r.callWithRetries(ctx, connection, address, stepID)
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
		webResults := make(map[string]string)
		var observations []json.RawMessage
		for _, call := range message.ToolCalls {
			if r.sandbox != nil && isSandboxTool(call.Function.Name) {
				if !sandboxLocked {
					locked, err := queries.TryLockRuntime(ctx, "sandbox:"+address.userID.String())
					if err != nil {
						return err
					}
					if !locked {
						if err := queries.DeferSandboxStep(ctx, reply.stepID); err != nil {
							return err
						}
						return errSandboxDeferred
					}
					sandboxLocked = true
					sandboxOwnedAt = time.Now()
					if err := queries.ClearSandboxStepDeferral(ctx, reply.stepID); err != nil {
						return err
					}
				}
				r.recordActivity(ctx, reply.stepID, call.Function.Name, "started", toolActivityDetail(call))
				result, err := r.executeSandboxTool(ctx, connection, address, reply.stepID, call)
				if err != nil {
					r.recordActivity(ctx, reply.stepID, call.Function.Name, "failed", "")
					return err
				}
				state := "returned"
				if strings.HasPrefix(result.Text, "Error:") {
					state = "failed"
				}
				r.recordActivity(ctx, reply.stepID, call.Function.Name, state, toolActivityDetail(call))
				webResults[call.ID] = result.Text
				if result.Image != "" {
					observation, err := json.Marshal(map[string]any{"role": "user", "name": "sandbox_observation", "content": []any{
						map[string]string{"type": "text", "text": "Current Linux desktop from tool call " + call.ID + ". This is a tool observation, not a new user request."},
						map[string]any{"type": "image_url", "image_url": map[string]string{"url": result.Image}},
					}})
					if err != nil {
						return err
					}
					observations = append(observations, observation)
				}
				continue
			}
			if call.Function.Name != "search" && call.Function.Name != "fetch_url" {
				continue
			}
			r.recordActivity(ctx, reply.stepID, call.Function.Name, "started", toolActivityDetail(call))
			result, err := whileRequest(ctx, connection, func(requestContext context.Context) (string, error) {
				if call.Function.Name == "search" {
					return r.webTools.search(requestContext, json.RawMessage(call.Function.Arguments))
				}
				return r.webTools.fetchURL(requestContext, json.RawMessage(call.Function.Arguments))
			})
			if ctx.Err() != nil || connection.Conn().IsClosed() {
				return errors.Join(ctx.Err(), err)
			}
			if err != nil {
				result = "Error: " + err.Error()
			}
			state := "returned"
			if err != nil {
				state = "failed"
			}
			r.recordActivity(ctx, reply.stepID, call.Function.Name, state, toolActivityDetail(call))
			webResults[call.ID] = result
		}
		if err := r.saveReply(ctx, connection, address, reply.stepID, reply.response, message, webResults, reply.delivery, observations...); err != nil {
			return err
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name == "assign_subagents" {
				unlockSandbox()
				break
			}
			if call.Function.Name == "send_message" {
				arguments, err := parseSendMessage(call)
				if err == nil && arguments.To != "user" && arguments.To != "main" {
					unlockSandbox()
					break
				}
			}
		}
		r.Wake(address.userID)
	}
}

type modelResult struct {
	response  chatResponse
	retryable bool
}

type modelReply struct {
	response chatResponse
	stepID   uuid.UUID
	delivery deliveryState
}

type storedModelReply struct {
	Response          chatResponse `json:"response"`
	DeliverySatisfied bool         `json:"delivery_satisfied"`
	DeliveryCorrected bool         `json:"delivery_corrected"`
}

func (r *Runner) callWithRetries(ctx context.Context, connection *pgxpool.Conn, address agentAddress, stepID uuid.UUID) (modelReply, error) {
	for attempt := 0; ; attempt++ {
		prepared, messages, err := r.prepareContext(ctx, connection, address)
		if err != nil {
			return modelReply{}, err
		}
		result, callErr := whileRequest(ctx, connection, func(requestContext context.Context) (modelResult, error) {
			response, retryable, err := r.requestModel(requestContext, prepared, database.StartModelCallParams{StepID: stepID, Purpose: "agent"})
			return modelResult{response: response, retryable: retryable}, err
		})
		if callErr == nil {
			return modelReply{response: result.response, stepID: stepID, delivery: inspectDelivery(messages, address)}, nil
		}
		if ctx.Err() != nil || connection.Conn().IsClosed() {
			return modelReply{}, callErr
		}
		errorText := callErr.Error()
		err = pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
			queries := database.New(tx)
			if err := queries.FinishStep(ctx, database.FinishStepParams{ID: stepID, Error: &errorText}); err != nil {
				return err
			}
			if result.retryable && attempt < 3 {
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
		if !result.retryable || attempt == 3 {
			return modelReply{}, callErr
		}
		select {
		case <-ctx.Done():
			return modelReply{}, ctx.Err()
		case <-time.After(time.Second << attempt):
		}
		stepID, err = r.prepareStep(ctx, connection, address)
		if err != nil {
			return modelReply{}, err
		}
	}
}

// HTTP 运行时连接空闲，检查锁连接的存活状态，失去执行权便取消请求。
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

func (r *Runner) saveReply(ctx context.Context, connection *pgxpool.Conn, address agentAddress, stepID uuid.UUID, response chatResponse, message assistantMessage, webResults map[string]string, delivery deliveryState, observations ...json.RawMessage) error {
	missingDelivery := len(message.ToolCalls) == 0 && hasPrivateText(message.Content) && !delivery.satisfied
	terminalFailure := missingDelivery && delivery.corrected
	err := pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, address.userID); err != nil {
			return err
		}
		if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: address.userID, SubagentID: address.subagentID, Role: "assistant", Content: response.Choices[0].Message, StepID: &stepID}); err != nil {
			return err
		}
		for _, call := range message.ToolCalls {
			result, isWeb := webResults[call.ID]
			if !isWeb {
				var err error
				result, err = r.executeDatabaseTool(ctx, queries, address, call)
				if err != nil {
					return err
				}
				state := "returned"
				if strings.HasPrefix(result, "Error:") {
					state = "failed"
				}
				activity, _ := json.Marshal(map[string]string{"kind": call.Function.Name, "state": state})
				if err := queries.RecordStepActivity(ctx, database.RecordStepActivityParams{StepID: stepID, Activity: activity}); err != nil {
					return err
				}
			}
			content, err := json.Marshal(map[string]string{"role": "tool", "tool_call_id": call.ID, "content": result})
			if err != nil {
				return err
			}
			if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: address.userID, SubagentID: address.subagentID, Role: "tool", Content: content}); err != nil {
				return err
			}
		}
		for _, observation := range observations {
			if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{UserID: address.userID, SubagentID: address.subagentID, Role: "user", Content: observation, StepID: &stepID}); err != nil {
				return err
			}
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

func (r *Runner) failAgent(ctx context.Context, connection *pgxpool.Conn, address agentAddress, cause error) error {
	return pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		queries := database.New(tx)
		errorText := cause.Error()
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

func failSubagent(ctx context.Context, queries *database.Queries, address agentAddress, cause error) error {
	if err := queries.SetSubagentStatus(ctx, database.SetSubagentStatusParams{UserID: address.userID, ID: *address.subagentID, Status: "failed"}); err != nil {
		return err
	}
	return deliverText(ctx, queries, agentAddress{userID: address.userID}, "system", address.subagentID, fmt.Sprintf("Subagent %s stopped: %s", address.subagentID, cause))
}
