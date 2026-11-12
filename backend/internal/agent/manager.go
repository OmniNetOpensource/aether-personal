// 本文件跨后端实例对等调度 Main 与后台 Subagent：Redis 传递独立唤醒队列、临时租约与打断/取消广播，PostgreSQL 保存全部持久状态。

package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/sandbox"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// ChatNotifyChannel 是跨实例聊天新消息与思考状态变更的 Redis 广播频道，仅携带用户 UUID。
const ChatNotifyChannel = "chat:notify"

const (
	agentWakeQueueKey  = "agent:wake_queue"
	agentInputChannel  = "agent:input"
	leaseTTL           = 30 * time.Second
	leaseRenewInterval = 10 * time.Second
	recoveryInterval   = 5 * time.Second
)

// enqueueWakeLua 在队列中尚无该标识时追加到队尾，避免高频输入或双实例补偿扫描重复入队。
const enqueueWakeLua = `
if redis.call("LPOS", KEYS[1], ARGV[1]) == false then
	return redis.call("RPUSH", KEYS[1], ARGV[1])
end
return 0
`

// renewLeaseLua 仅在当前实例仍持有原租约令牌时延长过期时间，防止覆盖新接管实例的租约。
const renewLeaseLua = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`

// releaseLeaseLua 仅删除当前实例自己持有的租约令牌，避免误删新实例刚抢到的租约。
const releaseLeaseLua = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`

// NotifyChat 向所有后端实例广播该用户的聊天状态已更新，唤醒持有其 WebSocket 的实例查表推送。
func NotifyChat(ctx context.Context, redisClient *redis.Client, userID uuid.UUID) error {
	return redisClient.Publish(ctx, ChatNotifyChannel, userID.String()).Err()
}

// leaseKey 生成单个用户 Main 执行租约的 Redis 键名。
func leaseKey(userID uuid.UUID) string {
	return "agent:lease:" + userID.String()
}

// HasActiveLease 检查指定用户的 Main 是否正被任一实例持有有效执行租约。
func (m *Manager) HasActiveLease(ctx context.Context, userID uuid.UUID) bool {
	if m == nil || m.redisClient == nil {
		return false
	}
	count, err := m.redisClient.Exists(ctx, leaseKey(userID)).Result()
	return err == nil && count > 0
}

// Manager 在对等实例内分别管理 Main 与 Subagent 的独立并发槽位和活跃 Runner，保证后台 Subagent 不会挤占 Main 响应用户消息的容量。
type Manager struct {
	databasePool  *pgxpool.Pool
	redisClient   *redis.Client
	settings      config.Config
	modelClient   *http.Client
	toolDeps      tools.Dependencies
	services      *services.Store
	slots         chan struct{}
	subagentSlots chan struct{}
	mutex         sync.Mutex
	runners       map[uuid.UUID]*Runner
	subagentStops map[uuid.UUID]context.CancelCauseFunc
	running       sync.WaitGroup
}

// NewManager 创建对等实例内的调度器；Main 与 Subagent 各自使用独立槽位池，满载时将剩余任务自然留给其他空闲实例。
func NewManager(pool *pgxpool.Pool, redisClient *redis.Client, settings config.Config, serviceSettings *services.Store, imageStore *images.Store) *Manager {
	capacity := int(settings.AgentCapacity)
	if capacity == 0 {
		capacity = max(1, int(pool.Config().MaxConns)-4)
	}
	return &Manager{
		databasePool: pool,
		redisClient:  redisClient,
		settings:     settings,
		services:     serviceSettings,
		modelClient:  &http.Client{Timeout: modelRequestTimeout},
		toolDeps: tools.Dependencies{
			DatabasePool: pool,
			Services:     serviceSettings,
			Images:       imageStore,
			HTTPClient:   &http.Client{Timeout: 120 * time.Second},
			Sandbox:      sandbox.New(pool, serviceSettings, imageStore, settings.Sandbox),
		},
		slots:         make(chan struct{}, capacity),
		subagentSlots: make(chan struct{}, capacity),
		runners:       map[uuid.UUID]*Runner{},
		subagentStops: map[uuid.UUID]context.CancelCauseFunc{},
	}
}

// Wake 在新输入或 subagent_event 提交后广播跨实例思考打断信号并将用户推入去重唤醒队列；使用独立超时避免客户端断线截断信号。
func (m *Manager) Wake(ctx context.Context, userID uuid.UUID, sequence int64) {
	m.mutex.Lock()
	runner := m.runners[userID]
	m.mutex.Unlock()
	if runner != nil {
		runner.newInput(sequence)
	}

	wakeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	userText := userID.String()
	pipeline := m.redisClient.Pipeline()
	pipeline.Publish(wakeCtx, agentInputChannel, fmt.Sprintf("%s:%d", userText, sequence))
	pipeline.Eval(wakeCtx, enqueueWakeLua, []string{agentWakeQueueKey}, userText)
	if _, err := pipeline.Exec(wakeCtx); err != nil {
		slog.Error("publish agent wake signal failed", "user_id", userID, "error", err)
	}
}

// Start 启动恢复持久待办，并并行运行输入打断监听、Subagent 取消监听、周期补偿以及 Main 和 Subagent 两条队列消费；退出时等待本地协程释放租约并交接。
func (m *Manager) Start(ctx context.Context) error {
	if err := m.recoverPending(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("recover pending agents: %w", err)
	}

	var workers sync.WaitGroup
	workers.Go(func() { m.listenInputSignals(ctx) })
	workers.Go(func() { m.listenSubagentCancelSignals(ctx) })
	workers.Go(func() { m.periodicRecovery(ctx) })
	workers.Go(func() { m.consumeSubagentQueue(ctx) })

	for {
		select {
		case <-ctx.Done():
			m.running.Wait()
			workers.Wait()
			return nil
		case m.slots <- struct{}{}:
		}

		popped, err := m.redisClient.BLPop(ctx, 5*time.Second, agentWakeQueueKey).Result()
		if err != nil {
			<-m.slots
			if errors.Is(err, redis.Nil) || ctx.Err() != nil {
				continue
			}
			slog.Error("pop agent wake queue failed", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		userID, err := uuid.Parse(popped[1])
		if err != nil {
			<-m.slots
			continue
		}
		m.tryStartRunner(ctx, userID)
	}
}

// consumeSubagentQueue 用独立槽位池阻塞消费后台 Subagent 唤醒队列，避免阻塞 Main 对用户的即时回复。
func (m *Manager) consumeSubagentQueue(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m.subagentSlots <- struct{}{}:
		}

		popped, err := m.redisClient.BLPop(ctx, 5*time.Second, subagentWakeQueueKey).Result()
		if err != nil {
			<-m.subagentSlots
			if errors.Is(err, redis.Nil) || ctx.Err() != nil {
				continue
			}
			slog.Error("pop subagent wake queue failed", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		subagentID, err := uuid.Parse(popped[1])
		if err != nil {
			<-m.subagentSlots
			continue
		}
		m.tryStartSubagentRunner(ctx, subagentID)
	}
}

// listenInputSignals 订阅跨实例的新输入序号广播；若该用户的 Main Runner 正在本实例思考，立即按本地取消句柄重读上下文。
func (m *Manager) listenInputSignals(ctx context.Context) {
	pubsub := m.redisClient.Subscribe(ctx, agentInputChannel)
	defer pubsub.Close()
	messages := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-messages:
			if !ok {
				return
			}
			rawUserID, rawSequence, found := strings.Cut(message.Payload, ":")
			if !found {
				continue
			}
			userID, userErr := uuid.Parse(rawUserID)
			sequence, seqErr := strconv.ParseInt(rawSequence, 10, 64)
			if userErr != nil || seqErr != nil {
				continue
			}
			m.mutex.Lock()
			runner := m.runners[userID]
			m.mutex.Unlock()
			if runner != nil {
				runner.newInput(sequence)
			}
		}
	}
}

// listenSubagentCancelSignals 订阅跨实例的 Subagent 取消广播；若目标 Subagent 正在本实例执行，立即中止其请求与工具。
func (m *Manager) listenSubagentCancelSignals(ctx context.Context) {
	pubsub := m.redisClient.Subscribe(ctx, subagentCancelChannel)
	defer pubsub.Close()
	messages := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-messages:
			if !ok {
				return
			}
			subagentID, err := uuid.Parse(message.Payload)
			if err != nil {
				continue
			}
			m.cancelLocalSubagent(subagentID)
		}
	}
}

// cancelLocalSubagent 触发本实例正在运行的指定 Subagent 立即退出。
func (m *Manager) cancelLocalSubagent(subagentID uuid.UUID) {
	m.mutex.Lock()
	stop := m.subagentStops[subagentID]
	m.mutex.Unlock()
	if stop != nil {
		stop(errSubagentCancelled)
	}
}

// broadcastSubagentCancel 在 Main 取消某个 Subagent 后立即打断本机与远端实例上的执行，并补推该用户排队中的其他 Subagent。
func (m *Manager) broadcastSubagentCancel(ctx context.Context, userID, subagentID uuid.UUID) {
	m.cancelLocalSubagent(subagentID)
	if err := m.redisClient.Publish(ctx, subagentCancelChannel, subagentID.String()).Err(); err != nil {
		slog.Error("publish subagent cancel failed", "subagent_id", subagentID, "error", err)
	}
	m.enqueuePendingUserSubagents(ctx, userID)
}

// periodicRecovery 定期扫描数据库持久待办，兜底 Redis 重启清空、瞬时网络抖动或并发槽位空出后的补位调度。
func (m *Manager) periodicRecovery(ctx context.Context) {
	ticker := time.NewTicker(recoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scanCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			err := m.recoverPending(scanCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("periodic agent recovery failed", "error", err)
			}
		}
	}
}

// recoverPending 对照数据库恢复无活跃租约的 Main 与后台 Subagent（按每用户并发上限排队入队）。
func (m *Manager) recoverPending(ctx context.Context) error {
	queries := database.New(m.databasePool)
	users, err := queries.ListMainWork(ctx)
	if err != nil {
		return err
	}
	for _, user := range users {
		m.mutex.Lock()
		runner := m.runners[user.UserID]
		m.mutex.Unlock()
		if runner != nil {
			runner.newInput(user.InputSequence)
			continue
		}
		exists, err := m.redisClient.Exists(ctx, leaseKey(user.UserID)).Result()
		if err != nil {
			return err
		}
		if exists == 0 {
			if err := m.enqueueWake(ctx, user.UserID); err != nil {
				return err
			}
		}
	}

	subagents, err := queries.ListRunningSubagents(ctx)
	if err != nil {
		return err
	}
	byUser := map[uuid.UUID][]uuid.UUID{}
	for _, sub := range subagents {
		byUser[sub.UserID] = append(byUser[sub.UserID], sub.ID)
	}
	for _, ids := range byUser {
		if err := m.enqueueUnleasedSubagentsUpToCap(ctx, ids); err != nil {
			return err
		}
	}
	return nil
}

// enqueuePendingUserSubagents 在单个用户的某个 Subagent 释放名额后，把该用户仍在等待并发名额的其余 Subagent 推入队列。
func (m *Manager) enqueuePendingUserSubagents(ctx context.Context, userID uuid.UUID) {
	rows, err := database.New(m.databasePool).ListUserSubagents(ctx, userID)
	if err != nil {
		slog.Error("list user subagents after release failed", "user_id", userID, "error", err)
		return
	}
	var runningIDs []uuid.UUID
	for _, row := range rows {
		if row.Status == "running" {
			runningIDs = append(runningIDs, row.ID)
		}
	}
	if len(runningIDs) == 0 {
		return
	}
	if err := m.enqueueUnleasedSubagentsUpToCap(ctx, runningIDs); err != nil {
		slog.Error("enqueue pending user subagents failed", "user_id", userID, "error", err)
	}
}

// enqueueUnleasedSubagentsUpToCap 检查一个用户全部 running Subagent 的租约占用数，仅为剩余并发名额内的无租约项补推队列。
func (m *Manager) enqueueUnleasedSubagentsUpToCap(ctx context.Context, runningIDs []uuid.UUID) error {
	active := 0
	var unleased []uuid.UUID
	for _, id := range runningIDs {
		exists, err := m.redisClient.Exists(ctx, subagentLeaseKey(id)).Result()
		if err != nil {
			return err
		}
		if exists > 0 {
			active++
		} else {
			unleased = append(unleased, id)
		}
	}
	available := maxConcurrentSubagentsPerUser - active
	for index := 0; index < len(unleased) && index < available; index++ {
		if err := m.enqueueSubagentWake(ctx, unleased[index]); err != nil {
			return err
		}
	}
	return nil
}

// enqueueWake 将待处理 Main 用户去重推入 Redis 唤醒列表。
func (m *Manager) enqueueWake(ctx context.Context, userID uuid.UUID) error {
	return m.redisClient.Eval(ctx, enqueueWakeLua, []string{agentWakeQueueKey}, userID.String()).Err()
}

// enqueueSubagentWake 将待处理 Subagent 去重推入 Redis Subagent 唤醒列表。
func (m *Manager) enqueueSubagentWake(ctx context.Context, subagentID uuid.UUID) error {
	return m.redisClient.Eval(ctx, enqueueWakeLua, []string{subagentWakeQueueKey}, subagentID.String()).Err()
}

// tryStartRunner 为已占用本机并发槽位的 Main 用户抢占 Redis 租约并递增数据库代数，成功后启动独占 Runner 与租约续期协程。
func (m *Manager) tryStartRunner(ctx context.Context, userID uuid.UUID) {
	key := leaseKey(userID)
	leaseToken := rand.Text()
	acquired, err := m.redisClient.SetNX(ctx, key, leaseToken, leaseTTL).Result()
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("acquire agent lease failed", "user_id", userID, "error", err)
		}
		<-m.slots
		return
	}
	if !acquired {
		<-m.slots
		return
	}

	var leaseEpoch int64
	var hasWork bool
	err = pgx.BeginFunc(ctx, m.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, userID); err != nil {
			return err
		}
		var err error
		hasWork, err = queries.HasMainWork(ctx, userID)
		if err != nil || !hasWork {
			return err
		}
		leaseEpoch, err = queries.ClaimUserLeaseEpoch(ctx, userID)
		return err
	})
	if err != nil || !hasWork {
		if err != nil && ctx.Err() == nil {
			slog.Error("claim user lease epoch failed", "user_id", userID, "error", err)
		}
		m.releaseKeyLease(key, leaseToken)
		if err == nil {
			m.requeueIfHasWork(userID)
		}
		<-m.slots
		return
	}

	runnerCtx, cancelRunner := context.WithCancelCause(ctx)
	runner := &Runner{
		manager:      m,
		databasePool: m.databasePool,
		redisClient:  m.redisClient,
		settings:     m.settings,
		modelClient:  m.modelClient,
		toolDeps:     m.toolDeps,
		services:     m.services,
		userID:       userID,
		leaseEpoch:   leaseEpoch,
	}
	m.mutex.Lock()
	m.runners[userID] = runner
	m.mutex.Unlock()
	m.running.Add(1)

	go func() {
		defer func() {
			<-m.slots
			m.running.Done()
		}()
		renewCtx, stopRenew := context.WithCancel(runnerCtx)
		renewDone := make(chan struct{})
		go func() {
			defer close(renewDone)
			m.keepKeyLeaseAlive(renewCtx, key, leaseToken, cancelRunner)
		}()

		runner.notifyChat()

		runErr := runner.run(runnerCtx)
		stopRenew()
		<-renewDone
		cancelRunner(nil)

		m.mutex.Lock()
		delete(m.runners, userID)
		m.mutex.Unlock()
		m.releaseKeyLease(key, leaseToken)

		runner.notifyChat()

		lostLease := errors.Is(runErr, errLostLease) || errors.Is(context.Cause(runnerCtx), errLostLease)
		if !lostLease {
			m.requeueIfHasWork(userID)
		}
		if runErr != nil && ctx.Err() == nil && !lostLease {
			slog.Error("agent run stopped", "user_id", userID, "error", runErr)
		}
	}()
}

// tryStartSubagentRunner 在单用户并发上限内为 Subagent 抢占 Redis 租约并递增代数，成功后启动后台 Runner 与续期协程。
func (m *Manager) tryStartSubagentRunner(ctx context.Context, subagentID uuid.UUID) {
	queries := database.New(m.databasePool)
	state, err := queries.GetSubagentLeaseState(ctx, subagentID)
	if err != nil || state.Status != "running" {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
			slog.Error("read subagent state failed", "subagent_id", subagentID, "error", err)
		}
		<-m.subagentSlots
		return
	}

	runningRows, err := queries.ListRunningSubagents(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("list running subagents for lease check failed", "subagent_id", subagentID, "error", err)
		}
		<-m.subagentSlots
		return
	}
	var userID uuid.UUID
	var siblingKeys []string
	for _, row := range runningRows {
		if row.ID == subagentID {
			userID = row.UserID
		}
	}
	if userID == uuid.Nil {
		<-m.subagentSlots
		return
	}
	for _, row := range runningRows {
		if row.UserID == userID {
			siblingKeys = append(siblingKeys, subagentLeaseKey(row.ID))
		}
	}

	key := subagentLeaseKey(subagentID)
	leaseToken := rand.Text()
	status, err := m.redisClient.Eval(ctx, acquireSubagentLeaseLua, siblingKeys, key, leaseToken, leaseTTL.Milliseconds(), maxConcurrentSubagentsPerUser).Int64()
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("acquire subagent lease failed", "subagent_id", subagentID, "error", err)
		}
		<-m.subagentSlots
		return
	}
	if status != 1 {
		<-m.subagentSlots
		return
	}

	var claimed database.ClaimSubagentLeaseEpochRow
	var stillRunning bool
	err = pgx.BeginFunc(ctx, m.databasePool, func(tx pgx.Tx) error {
		txQueries := database.New(tx)
		if err := txQueries.LockSubagent(ctx, subagentID); err != nil {
			return err
		}
		var err error
		claimed, err = txQueries.ClaimSubagentLeaseEpoch(ctx, subagentID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		stillRunning = true
		return nil
	})
	if err != nil || !stillRunning {
		if err != nil && ctx.Err() == nil {
			slog.Error("claim subagent lease epoch failed", "subagent_id", subagentID, "error", err)
		}
		m.releaseKeyLease(key, leaseToken)
		<-m.subagentSlots
		return
	}

	runnerCtx, cancelRunner := context.WithCancelCause(ctx)
	idCopy := subagentID
	runner := &Runner{
		manager:      m,
		databasePool: m.databasePool,
		redisClient:  m.redisClient,
		settings:     m.settings,
		modelClient:  m.modelClient,
		toolDeps:     m.toolDeps,
		services:     m.services,
		userID:       claimed.UserID,
		subagentID:   &idCopy,
		startedAt:    claimed.StartedAt,
		leaseEpoch:   claimed.LeaseEpoch,
	}
	m.mutex.Lock()
	m.subagentStops[subagentID] = cancelRunner
	m.mutex.Unlock()
	m.running.Add(1)

	go func() {
		defer func() {
			<-m.subagentSlots
			m.running.Done()
		}()
		renewCtx, stopRenew := context.WithCancel(runnerCtx)
		renewDone := make(chan struct{})
		go func() {
			defer close(renewDone)
			m.keepKeyLeaseAlive(renewCtx, key, leaseToken, cancelRunner)
		}()

		runner.notifyChat()

		runErr := runner.run(runnerCtx)
		stopRenew()
		<-renewDone
		cancelRunner(nil)

		m.mutex.Lock()
		delete(m.subagentStops, subagentID)
		m.mutex.Unlock()
		m.releaseKeyLease(key, leaseToken)

		runner.notifyChat()

		lostLease := errors.Is(runErr, errLostLease) || errors.Is(context.Cause(runnerCtx), errLostLease)
		cancelled := errors.Is(context.Cause(runnerCtx), errSubagentCancelled)
		if !lostLease && !cancelled {
			m.requeueSubagentIfRunning(claimed.UserID, subagentID)
		} else {
			cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
			m.enqueuePendingUserSubagents(cleanup, claimed.UserID)
			stop()
		}
		if runErr != nil && ctx.Err() == nil && !lostLease && !cancelled {
			slog.Error("subagent run stopped", "user_id", claimed.UserID, "subagent_id", subagentID, "error", runErr)
		}
	}()
}

// keepKeyLeaseAlive 在 Runner 运行期间定时续期 Redis 租约；租约被取代或超期无法续上时立即中止本机执行。
func (m *Manager) keepKeyLeaseAlive(ctx context.Context, key, leaseToken string, cancelRunner context.CancelCauseFunc) {
	ticker := time.NewTicker(leaseRenewInterval)
	defer ticker.Stop()
	lastRenewed := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewed, err := m.redisClient.Eval(ctx, renewLeaseLua, []string{key}, leaseToken, leaseTTL.Milliseconds()).Int64()
			if err != nil {
				if ctx.Err() == nil && time.Since(lastRenewed) >= leaseTTL {
					cancelRunner(errLostLease)
					return
				}
				continue
			}
			if renewed == 0 {
				cancelRunner(errLostLease)
				return
			}
			lastRenewed = time.Now()
		}
	}
}

// releaseKeyLease 使用独立短超时释放当前实例持有的 Redis 租约，保证停机信号到达后也能立刻让位给存活实例。
func (m *Manager) releaseKeyLease(key, leaseToken string) {
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.redisClient.Eval(cleanup, releaseLeaseLua, []string{key}, leaseToken).Err(); err != nil {
		slog.Error("release agent lease failed", "lease_key", key, "error", err)
	}
}

// requeueIfHasWork 在释放 Main 租约后复查数据库持久待办：有未完工作（含停机中断或退出临界区到达的新输入）时立即重新入队。
func (m *Manager) requeueIfHasWork(userID uuid.UUID) {
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	hasWork, err := database.New(m.databasePool).HasMainWork(cleanup, userID)
	if err != nil {
		slog.Error("recheck Main work after release failed", "user_id", userID, "error", err)
		return
	}
	if hasWork {
		if err := m.enqueueWake(cleanup, userID); err != nil {
			slog.Error("requeue pending user failed", "user_id", userID, "error", err)
		}
	}
}

// requeueSubagentIfRunning 在 Subagent 释放租约后复查其持久状态（如滚动停机中断），并顺带补推该用户排队的其余 Subagent。
func (m *Manager) requeueSubagentIfRunning(userID, subagentID uuid.UUID) {
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stillRunning, err := database.New(m.databasePool).HasSubagentWork(cleanup, subagentID)
	if err != nil {
		slog.Error("recheck subagent work after release failed", "subagent_id", subagentID, "error", err)
		return
	}
	if stillRunning {
		if err := m.enqueueSubagentWake(cleanup, subagentID); err != nil {
			slog.Error("requeue interrupted subagent failed", "subagent_id", subagentID, "error", err)
		}
		return
	}
	m.enqueuePendingUserSubagents(cleanup, userID)
}
