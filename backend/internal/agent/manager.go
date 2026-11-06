// 本文件在单个后端进程内调度 Main：消息提交后直接唤醒，每用户最多一个 Runner，启动与周期补查恢复持久待办。

package agent

import (
	"context"
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

// 补查只负责找回提交与唤醒之间的中断；正常输入和名额释放都立即触发调度。
const recoveryInterval = 5 * time.Second
const agentDrainTimeout = 15 * time.Second

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
	pending      map[uuid.UUID]struct{}
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
		runners: map[uuid.UUID]*Runner{}, pending: map[uuid.UUID]struct{}{}}
}

// Wake 在输入提交后登记最新序号并立即打断尚未输出的思考；只发合并信号，不等待数据库或模型。
// 运行中的用户也保留待唤醒标记，直到 Runner 注销后再消费，因此退出与新输入交错时不会漏跑。
func (m *Manager) Wake(userID uuid.UUID, sequence int64) {
	m.mutex.Lock()
	if _, queued := m.pending[userID]; !queued {
		m.queue = append(m.queue, userID)
	}
	m.pending[userID] = struct{}{}
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
func (m *Manager) Start(ctx context.Context) {
	runtimeCtx, cancelRuntime := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRuntime()
	ticker := time.NewTicker(recoveryInterval)
	defer ticker.Stop()
	m.recoverPending(runtimeCtx)
	m.signal()
	shutdown := ctx.Done()
	var drainTimer *time.Timer
	var drainDeadline <-chan time.Time
	for {
		select {
		case <-shutdown:
			shutdown = nil
			drainTimer = time.NewTimer(agentDrainTimeout)
			drainDeadline = drainTimer.C
		case <-drainDeadline:
			goto finished
		case <-ticker.C:
			if ctx.Err() == nil {
				m.recoverPending(runtimeCtx)
			}
		case <-m.wakeSignal:
			if ctx.Err() == nil {
				m.startPending(runtimeCtx)
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
	cancelRuntime()
	m.running.Wait()
}

// recoverPending 从持久事实找回未处理输入和任务，也用最新序号补上遗漏的新输入打断。
func (m *Manager) recoverPending(ctx context.Context) {
	operation, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	users, err := database.New(m.databasePool).ListMainWork(operation)
	if err != nil {
		slog.Error("recover pending agents failed", "error", err)
		return
	}
	for _, user := range users {
		m.Wake(user.UserID, user.InputSequence)
	}
}

// startPending 按首次唤醒顺序分配本地名额；活跃用户的补充标记留到注销，等待用户不会被它阻塞。
func (m *Manager) startPending(ctx context.Context) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	waiting := m.queue[:0]
	for _, userID := range m.queue {
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
			runner.run(ctx)
			m.mutex.Lock()
			delete(m.runners, userID)
			m.mutex.Unlock()
			m.signal()
		}()
	}
	m.queue = waiting
}
