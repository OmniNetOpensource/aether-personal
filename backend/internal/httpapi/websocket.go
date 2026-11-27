// 本文件是已登录用户的聊天实时推送通道：首帧校验会话并确认同步游标，之后借助 Redis 广播跨实例唤醒推送增量消息与思考状态。

package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// helloFrame 是前端建连后发出的唯一上行帧：用令牌鉴权并携带断点游标，之后连接只用于服务端推送。
type helloFrame struct {
	Type             string `json:"type"`
	Token            string `json:"token"`
	LastChangeCursor *int64 `json:"last_change_cursor,string"`
}

// chatConnections 记录本实例每个用户的 WebSocket 连接；收到该用户的 Redis 推送广播时唤醒这些连接去查表推送。
type chatConnections struct {
	mutex       sync.Mutex
	wakeSignals map[uuid.UUID]map[chan struct{}]struct{}
}

// newChatConnections 创建空的连接注册表，供每条新连接登记自己的唤醒通道。
func newChatConnections() *chatConnections {
	return &chatConnections{wakeSignals: map[uuid.UUID]map[chan struct{}]struct{}{}}
}

// add 为一条连接登记唤醒通道并返回它；通道带 1 的缓冲，短时间内的多次唤醒会合并成一次。
func (c *chatConnections) add(userID uuid.UUID) chan struct{} {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	wakeSignal := make(chan struct{}, 1)
	if c.wakeSignals[userID] == nil {
		c.wakeSignals[userID] = map[chan struct{}]struct{}{}
	}
	c.wakeSignals[userID][wakeSignal] = struct{}{}
	return wakeSignal
}

// remove 注销一条连接的唤醒通道；该用户再无连接时连带删掉其条目，避免注册表只增不减。
func (c *chatConnections) remove(userID uuid.UUID, wakeSignal chan struct{}) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	delete(c.wakeSignals[userID], wakeSignal)
	if len(c.wakeSignals[userID]) == 0 {
		delete(c.wakeSignals, userID)
	}
}

// wake 唤醒某个用户的全部连接去查表推送；通道已满说明已有待处理的唤醒，非阻塞发送直接跳过即可。
func (c *chatConnections) wake(userID uuid.UUID) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for wakeSignal := range c.wakeSignals[userID] {
		select {
		case wakeSignal <- struct{}{}:
		default:
		}
	}
}

// wakeAll 唤醒全部连接；监听（重）连成功后调用，让各连接补查未监听期间错过的消息。
func (c *chatConnections) wakeAll() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for _, userWakeSignals := range c.wakeSignals {
		for wakeSignal := range userWakeSignals {
			select {
			case wakeSignal <- struct{}{}:
			default:
			}
		}
	}
}

// listenForUserMessages 订阅 Redis 聊天推送频道；首次连接与断线重连后唤醒全部连接补查数据库，不占用 PostgreSQL 长连接。
func (s *server) listenForUserMessages(ctx context.Context) {
	pubsub := s.redisClient.Subscribe(ctx, agent.ChatNotifyChannel)
	defer pubsub.Close()
	events := pubsub.ChannelWithSubscriptions()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			switch item := event.(type) {
			case *redis.Subscription:
				if item.Kind == "subscribe" {
					s.connections.wakeAll()
				}
			case *redis.Message:
				if userID, err := uuid.Parse(item.Payload); err == nil {
					s.connections.wake(userID)
				}
			}
		}
	}
}

// chatWebSocket 处理已登录用户的单向实时推送连接：首帧 hello 认证后交由 CloseRead 维持控制帧，主循环只响应唤醒与心跳。
func (s *server) chatWebSocket(w http.ResponseWriter, r *http.Request) {
	// 鉴权靠 hello 中的令牌而非 Cookie，跨站页面无法冒用身份，因此不校验 Origin（Web 客户端可能部署在其他域名）。
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(64 << 10)
	relay := newEventRelay(connection)

	ctx := r.Context()
	// 浏览器无法给 WebSocket 握手附带 Authorization 头，也避免将令牌放在 URL 中进入访问日志，故由首帧 hello 携带。
	helloContext, cancelHello := context.WithTimeout(ctx, 5*time.Second)
	var hello helloFrame
	err = wsjson.Read(helloContext, connection, &hello)
	cancelHello()
	if err != nil || hello.Type != "hello" || (hello.LastChangeCursor != nil && *hello.LastChangeCursor < 0) {
		connection.Close(websocket.StatusPolicyViolation, "hello required")
		return
	}
	if hello.Token == "" {
		connection.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}

	session, err := s.findSession(ctx, hello.Token)
	if errors.Is(err, pgx.ErrNoRows) {
		connection.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}
	if err != nil {
		slog.Error("find session for websocket failed", "error", err)
		connection.Close(websocket.StatusInternalError, "internal server error")
		return
	}

	var changeCursor int64
	if hello.LastChangeCursor != nil {
		changeCursor = *hello.LastChangeCursor
	} else {
		changeCursor, err = s.queries.GetLatestChatChange(ctx, session.UserID)
		if err != nil {
			slog.Error("load chat cursor failed", "user_id", session.UserID, "error", err)
			connection.Close(websocket.StatusInternalError, "internal server error")
			return
		}
	}

	wakeSignal := s.connections.add(session.UserID)
	defer s.connections.remove(session.UserID, wakeSignal)

	if err := relay.send(ctx, readyEvent{Type: "ready", ChangeCursor: changeCursor}); err != nil {
		return
	}
	if err := s.pushChatMessages(ctx, relay, session.UserID, &changeCursor); err != nil {
		return
	}

	// 握手完成后不再接受任何业务上行帧；CloseRead 自动处理 Ping/Pong/Close 控制帧并在对端断开时取消 ctx。
	ctx = connection.CloseRead(ctx)

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wsCtx.Done():
			connection.Close(websocket.StatusGoingAway, "server shutting down")
			return
		case <-wakeSignal:
			if err := s.pushChatMessages(ctx, relay, session.UserID, &changeCursor); err != nil {
				return
			}
		case <-heartbeat.C:
			pingContext, cancelPing := context.WithTimeout(ctx, 10*time.Second)
			err := connection.Ping(pingContext)
			cancelPing()
			if err != nil {
				return
			}
			if err := s.pushChatMessages(ctx, relay, session.UserID, &changeCursor); err != nil {
				return
			}
		}
	}
}

// pushChatMessages 按变更序号补发消息，并把本次读到的游标交给连接用于重连；同一事件还附带 Main 状态，供前端一次更新列表与运行指示。
func (s *server) pushChatMessages(ctx context.Context, relay *eventRelay, userID uuid.UUID, changeCursor *int64) error {
	// 单页查询上限 100 行，循环取到不满页为止，避免积压较多时漏掉后面的消息。
	var rows []database.ListChatRowsAfterRow
	for {
		page, err := s.queries.ListChatRowsAfter(ctx, database.ListChatRowsAfterParams{UserID: userID, AfterCursor: *changeCursor})
		if err != nil {
			slog.Error("list chat rows failed", "user_id", userID, "error", err)
			return err
		}
		rows = append(rows, page...)
		if len(page) > 0 {
			*changeCursor = page[len(page)-1].ChangeCursor
		}
		if len(page) < 100 {
			break
		}
	}

	thinking, failed, err := s.loadMainStatus(ctx, userID)
	if err != nil {
		slog.Error("get Main activity failed", "user_id", userID, "error", err)
		return err
	}
	return relay.send(ctx, messagesEvent{
		Type: "messages", MainThinking: thinking, MainFailed: failed, ChangeCursor: *changeCursor,
		chatMessages: chatMessages{Messages: s.chatMessagesWithPreviews(ctx, rows)},
	})
}
