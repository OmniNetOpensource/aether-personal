// 本文件跨后端实例对等调度 Main：Redis 传递唤醒队列、用户临时租约与思考打断信号，PostgreSQL 保存全部持久状态。

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

// enqueueWakeLua 在队列中尚无该用户时追加到队尾，避免高频输入或双实例补偿扫描把同一用户重复入队。
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

// Manager 在对等实例内管理本地并发槽位与活跃 Runner：跨实例排队靠 Redis 阻塞列表，本机字典仅用于即时打断思考中的模型请求。
type Manager struct {
	databasePool *pgxpool.Pool
	redisClient  *redis.Client
	settings     config.Config
	modelClient  *http.Client
	toolDeps     tools.Dependencies
	services     *services.Store
	slots        chan struct{}
	mutex        sync.Mutex
	runners      map[uuid.UUID]*Runner
	running      sync.WaitGroup
}

// NewManager 创建对等实例内的调度器；容量槽位满载时暂停从 Redis 取活，将剩余用户自然留给其他空闲实例。
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
		slots:   make(chan struct{}, capacity),
		runners: map[uuid.UUID]*Runner{},
	}
}

// Wake 在新输入消息提交后单次往返广播聊天推送、跨实例思考打断信号与去重唤醒队列；使用独立超时避免客户端断线截断信号。
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
	pipeline.Publish(wakeCtx, ChatNotifyChannel, userText)
	pipeline.Publish(wakeCtx, agentInputChannel, fmt.Sprintf("%s:%d", userText, sequence))
	pipeline.Eval(wakeCtx, enqueueWakeLua, []string{agentWakeQueueKey}, userText)
	if _, err := pipeline.Exec(wakeCtx); err != nil {
		slog.Error("publish agent wake signal failed", "user_id", userID, "error", err)
	}
}

// Start 启动恢复持久待办，并并行运行思考打断监听、周期补偿与队列阻塞消费；退出时等待本地 Runner 释放租约并交接未完工作。
func (m *Manager) Start(ctx context.Context) error {
	if err := m.recoverPending(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("recover pending agents: %w", err)
	}

	var workers sync.WaitGroup
	workers.Go(func() { m.listenInputSignals(ctx) })
	workers.Go(func() { m.periodicRecovery(ctx) })

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

// listenInputSignals 订阅跨实例的新输入序号广播；若该用户的 Runner 正在本实例思考，立即按本地取消句柄重读上下文。
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

// periodicRecovery 定期扫描数据库持久待办，兜底 Redis 重启清空或瞬时网络抖动遗漏的唤醒与打断信号。
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

// recoverPending 对照数据库 main_work 补发本地打断，并将当前无活跃租约的待办用户推进 Redis 队列。
func (m *Manager) recoverPending(ctx context.Context) error {
	users, err := database.New(m.databasePool).ListMainWork(ctx)
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
	return nil
}

// enqueueWake 将待处理用户去重推入 Redis 唤醒列表。
func (m *Manager) enqueueWake(ctx context.Context, userID uuid.UUID) error {
	return m.redisClient.Eval(ctx, enqueueWakeLua, []string{agentWakeQueueKey}, userID.String()).Err()
}

// tryStartRunner 为已占用本机并发槽位的用户抢占 Redis 租约并递增数据库代数，成功后启动独占 Runner 与租约续期协程。
func (m *Manager) tryStartRunner(ctx context.Context, userID uuid.UUID) {
	leaseToken := rand.Text()
	acquired, err := m.redisClient.SetNX(ctx, leaseKey(userID), leaseToken, leaseTTL).Result()
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

	// 在用户消息锁内确认待办并递增代数，使此前任何超时残留实例的写库事务立即失效。
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
		m.releaseLease(userID, leaseToken)
		if err == nil {
			m.requeueIfHasWork(userID)
		}
		<-m.slots
		return
	}

	runnerCtx, cancelRunner := context.WithCancelCause(ctx)
	runner := &Runner{
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
			m.keepLeaseAlive(renewCtx, userID, leaseToken, cancelRunner)
		}()

		runErr := runner.run(runnerCtx)
		stopRenew()
		<-renewDone
		cancelRunner(nil)

		// 先注销本机字典再释放 Redis 租约；随后复查数据库待办，覆盖退出瞬间到达的新输入与滚动停机交接。
		m.mutex.Lock()
		delete(m.runners, userID)
		m.mutex.Unlock()
		m.releaseLease(userID, leaseToken)

		lostLease := errors.Is(runErr, errLostLease) || errors.Is(context.Cause(runnerCtx), errLostLease)
		if !lostLease {
			m.requeueIfHasWork(userID)
		}
		if runErr != nil && ctx.Err() == nil && !lostLease {
			slog.Error("agent run stopped", "user_id", userID, "error", runErr)
		}
	}()
}

// keepLeaseAlive 在 Runner 运行期间定时续期 Redis 租约；租约被取代或超期无法续上时立即中止本机执行。
func (m *Manager) keepLeaseAlive(ctx context.Context, userID uuid.UUID, leaseToken string, cancelRunner context.CancelCauseFunc) {
	ticker := time.NewTicker(leaseRenewInterval)
	defer ticker.Stop()
	lastRenewed := time.Now()
	key := leaseKey(userID)
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

// releaseLease 使用独立短超时释放当前实例持有的 Redis 租约，保证停机信号到达后也能立刻让位给存活实例。
func (m *Manager) releaseLease(userID uuid.UUID, leaseToken string) {
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.redisClient.Eval(cleanup, releaseLeaseLua, []string{leaseKey(userID)}, leaseToken).Err(); err != nil {
		slog.Error("release agent lease failed", "user_id", userID, "error", err)
	}
}

// requeueIfHasWork 在释放租约后复查数据库持久待办：有未完工作（含停机中断或退出临界区到达的新输入）时立即重新入队。
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
