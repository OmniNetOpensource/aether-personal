// 本文件在单进程内调度 Main：消息提交后直接唤醒，启动时恢复持久待办，临时失败仅延迟重试对应用户。

package agent

import (
	"context"
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
	"github.com/jackc/pgx/v5/pgxpool"
)

// 数据库故障退避最多十五秒；正常退出也给正在运行的任务十五秒收尾。
const maxDatabaseRetryDelay = 15 * time.Second
const agentDrainTimeout = 15 * time.Second

// pendingUser 区分正常唤醒和临时失败退避；等待重试不占并发名额，新输入可立即重新调度。
type pendingUser struct {
	retryAt    time.Time
	retryDelay time.Duration
}

// Manager 共享进程依赖，用同一把互斥锁登记 Runner 和待唤醒用户，防止空闲退出时漏掉新输入。
type Manager struct {
	databasePool *pgxpool.Pool
	settings     config.Config
	modelClient  *http.Client
	toolDeps     tools.Dependencies
	services     *services.Store
	capacity     int
	wakeSignal   chan struct{}
	mutex        sync.Mutex
	runners      map[uuid.UUID]*Runner
	pending      map[uuid.UUID]pendingUser
	queue        []uuid.UUID
	running      sync.WaitGroup
}

// NewManager 创建唯一的进程内调度器；并发用户共享客户端和连接池，不常驻空闲 Runner。
func NewManager(pool *pgxpool.Pool, settings config.Config, serviceSettings *services.Store) *Manager {
	capacity := int(settings.AgentCapacity)
	if capacity == 0 {
		capacity = max(1, int(pool.Config().MaxConns)-4)
	}
	return &Manager{databasePool: pool, settings: settings, services: serviceSettings,
		modelClient: &http.Client{Timeout: modelRequestTimeout},
		toolDeps: tools.Dependencies{DatabasePool: pool, Services: serviceSettings,
			HTTPClient: &http.Client{Timeout: 120 * time.Second}},
		capacity: capacity, wakeSignal: make(chan struct{}, 1),
		runners: map[uuid.UUID]*Runner{}, pending: map[uuid.UUID]pendingUser{}}
}

// Wake 在输入提交后登记待办，并用输入序号打断尚未输出的思考；只发合并信号，不等待数据库或模型。
// 运行中的用户也保留待唤醒标记，直到 Runner 注销后再消费，因此退出与新输入交错时不会漏跑。
func (m *Manager) Wake(userID uuid.UUID, sequence int64) {
	m.mutex.Lock()
	if _, queued := m.pending[userID]; !queued {
		m.queue = append(m.queue, userID)
	}
	m.pending[userID] = pendingUser{}
	if runner := m.runners[userID]; runner != nil {
		runner.newInput(sequence)
	}
	m.mutex.Unlock()
	m.signal()
}

// signal 合并调度请求；待办用户另存在受锁保护的队列中，不因信号合并丢失。
func (m *Manager) signal() {
	select {
	case m.wakeSignal <- struct{}{}:
	default:
	}
}

// Start 恢复数据库待办并消费进程内唤醒；退出时停止创建 Runner，给已有执行十五秒完成，再取消并等待收尾。
func (m *Manager) Start(ctx context.Context) error {
	if err := m.recoverPending(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	runtimeCtx, cancelRuntime := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRuntime()
	m.signal()
	shutdown := ctx.Done()
	var drainTimer *time.Timer
	var drainDeadline <-chan time.Time
	var retryTimer *time.Timer
	var retryReady <-chan time.Time
	for {
		select {
		case <-shutdown:
			shutdown = nil
			drainTimer = time.NewTimer(agentDrainTimeout)
			drainDeadline = drainTimer.C
		case <-drainDeadline:
			goto finished
		case <-retryReady:
			retryReady = nil
			m.signal()
		case <-m.wakeSignal:
			if ctx.Err() == nil {
				retryAt := m.startPending(runtimeCtx)
				// 只为最近一个失败用户的到期时间设闹钟，没有失败用户时不保留定时器。
				if retryTimer != nil {
					retryTimer.Stop()
				}
				retryReady = nil
				if !retryAt.IsZero() {
					if retryTimer == nil {
						retryTimer = time.NewTimer(time.Until(retryAt))
					} else {
						retryTimer.Reset(time.Until(retryAt))
					}
					retryReady = retryTimer.C
				}
			}
		}
		m.mutex.Lock()
		empty := len(m.runners) == 0
		m.mutex.Unlock()
		if ctx.Err() != nil && empty {
			break
		}
	}
finished:
	if drainTimer != nil {
		drainTimer.Stop()
	}
	if retryTimer != nil {
		retryTimer.Stop()
	}
	cancelRuntime()
	m.running.Wait()
	return nil
}

// recoverPending 只在启动时读取一次成功的待办快照；读取本身临时失败时退避重试，成功后不再全局扫描。
func (m *Manager) recoverPending(ctx context.Context) error {
	delay := time.Second
	for {
		operation, cancel := context.WithTimeout(ctx, 3*time.Second)
		users, err := database.New(m.databasePool).ListMainWork(operation)
		cancel()
		if err == nil {
			for _, user := range users {
				m.Wake(user.UserID, user.InputSequence)
			}
			return nil
		}
		if ctx.Err() != nil || !temporaryDatabaseError(err) {
			return fmt.Errorf("recover pending agents: %w", err)
		}
		slog.Error("recover pending agents failed; retrying startup", "error", err, "retry_delay", delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, maxDatabaseRetryDelay)
	}
}

// startPending 按首次唤醒顺序分配本地名额；活跃用户的补充标记留到注销，等待用户不会被它阻塞。
func (m *Manager) startPending(ctx context.Context) (retryAt time.Time) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	waiting := m.queue[:0]
	for _, userID := range m.queue {
		pending := m.pending[userID]
		if time.Until(pending.retryAt) > 0 {
			waiting = append(waiting, userID)
			if retryAt.IsZero() || pending.retryAt.Before(retryAt) {
				retryAt = pending.retryAt
			}
			continue
		}
		if m.runners[userID] != nil || len(m.runners) >= m.capacity {
			waiting = append(waiting, userID)
			continue
		}
		delete(m.pending, userID)
		runner := &Runner{databasePool: m.databasePool, settings: m.settings,
			modelClient: m.modelClient, toolDeps: m.toolDeps, services: m.services, userID: userID}
		m.runners[userID] = runner
		m.running.Add(1)
		go func() {
			defer m.running.Done()
			err := runner.run(ctx)
			retrying := err != nil && ctx.Err() == nil && temporaryDatabaseError(err)
			m.mutex.Lock()
			delete(m.runners, userID)
			// 失败用户重新排到队尾并让出名额；若期间已有新输入，沿用其立即唤醒，不覆盖为退避。
			if _, queued := m.pending[userID]; retrying && !queued {
				delay := min(max(pending.retryDelay*2, time.Second), maxDatabaseRetryDelay)
				m.pending[userID] = pendingUser{retryAt: time.Now().Add(delay), retryDelay: delay}
				m.queue = append(m.queue, userID)
			}
			m.mutex.Unlock()
			if err != nil && ctx.Err() == nil {
				slog.Error("agent run stopped", "user_id", userID, "retrying", retrying, "error", err)
			}
			m.signal()
		}()
	}
	m.queue = waiting
	return retryAt
}
