// 本文件是聊天 WebSocket 主通道：处理登录与发送等客户端命令、推送历史与新消息，并借助数据库通知跨连接唤醒推送。

package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// maxMessageCharacters 是单条用户消息的字符上限，发送命令的校验以此为准。
const maxMessageCharacters = 32000

// clientFrame 是前端通过聊天 WebSocket 发来的全部命令的统一形状；
// 后端发出的事件类型见 events.go。
type clientFrame struct {
	Type            string     `json:"type"`
	RequestID       string     `json:"request_id"`
	Token           string     `json:"token"`
	LastPosition    *uuid.UUID `json:"last_position"`
	Email           string     `json:"email"`
	Code            string     `json:"code"`
	Before          *uuid.UUID `json:"before"`
	ClientMessageID uuid.UUID  `json:"client_message_id"`
	Text            string     `json:"text"`
}

// chatConnections 记录本机每个用户的 WebSocket 连接；收到该用户的 NOTIFY 时唤醒这些连接去查表推送。
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

// listenForUserMessages 常驻监听数据库的 user_messages 通知；一轮监听断开后隔一秒重连，避免数据库不可用时空转重试。
func (s *server) listenForUserMessages() {
	for {
		err := s.listenOnce(context.Background())
		slog.Error("listen for user messages stopped", "error", err)
		time.Sleep(time.Second)
	}
}

// listenOnce 用一条独占连接完成一轮 LISTEN；通知的载荷就是用户 ID，据此唤醒该用户的连接推送并推进其 agent 运行。
func (s *server) listenOnce(ctx context.Context) error {
	pooledConnection, err := s.databasePool.Acquire(ctx)
	if err != nil {
		return err
	}
	// 监听中的连接不能还回连接池。
	connection := pooledConnection.Hijack()
	defer connection.Close(ctx)

	if _, err := connection.Exec(ctx, "LISTEN user_messages"); err != nil {
		return err
	}
	// 未监听期间的通知已经丢失，让所有连接各自查一次表。
	s.connections.wakeAll()
	for {
		notification, err := connection.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		s.connections.wake(uuid.MustParse(notification.Payload))
		s.runner.Wake(uuid.MustParse(notification.Payload))
	}
}

// chatWebSocket 处理一条聊天连接的整个生命周期：先经 hello 恢复会话或等待登录，再在主循环里交替做新消息推送、心跳保活与客户端命令分发。
func (s *server) chatWebSocket(w http.ResponseWriter, r *http.Request) {
	// 登录靠 hello 中的令牌而非 Cookie，跨站页面无法冒用身份，因此不校验 Origin（Web 客户端可能部署在其他域名）。
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	// 32,000 个字符的 UTF-8 编码最多约 128 KB。
	connection.SetReadLimit(256 << 10)
	relay := newEventRelay(connection)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// 连接建立后前端立刻发 hello 表明身份；未登录也能连上，登录本身走这条连接。
	helloContext, cancelHello := context.WithTimeout(ctx, 5*time.Second)
	var hello clientFrame
	err = wsjson.Read(helloContext, connection, &hello)
	cancelHello()
	if err != nil || hello.Type != "hello" {
		connection.Close(websocket.StatusPolicyViolation, "hello required")
		return
	}

	// 以下连接状态只由本函数的主循环读写，接收 goroutine 只负责转发帧。
	var userID, sessionID uuid.UUID
	var email string
	var position uuid.UUID
	var wakeSignal chan struct{}
	authenticated := false
	bindSession := func(session database.GetActiveSessionRow) {
		userID, sessionID, email, authenticated = session.UserID, session.ID, session.Email, true
		wakeSignal = s.connections.add(userID)
	}
	unbindSession := func() {
		if wakeSignal != nil {
			s.connections.remove(userID, wakeSignal)
			wakeSignal = nil
		}
		userID, sessionID, email, position, authenticated = uuid.Nil, uuid.Nil, "", uuid.Nil, false
	}
	defer func() {
		if wakeSignal != nil {
			s.connections.remove(userID, wakeSignal)
		}
	}()

	// hello 带了令牌就尝试恢复会话：前端给了断点位置则从断点续推，没给则定位到最新消息、改由 ready 一并下发首屏历史。
	devLogin := s.appConfig.AppEnv == "dev"
	if hello.Token != "" {
		session, err := s.findSession(ctx, hello.Token)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("find session failed", "error", err)
			connection.Close(websocket.StatusInternalError, "internal server error")
			return
		}
		if err == nil {
			bindSession(session)
			if hello.LastPosition != nil {
				position = *hello.LastPosition
			} else if latest, err := s.queries.GetLatestMainMessageID(ctx, userID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				slog.Error("get latest message failed", "error", err)
				connection.Close(websocket.StatusInternalError, "internal server error")
				return
			} else if err == nil {
				position = latest
			}
		}
	}

	ready := readyEvent{Type: "ready", Authenticated: authenticated, DevLogin: devLogin, Email: email}
	if authenticated && hello.LastPosition == nil {
		history, err := s.historyPage(ctx, userID, uuid.Max)
		if err != nil {
			slog.Error("load initial history failed", "error", err)
			connection.Close(websocket.StatusInternalError, "internal server error")
			return
		}
		ready.History = &history
	}
	if err := relay.send(ctx, ready); err != nil {
		return
	}
	if authenticated {
		if err := s.pushChatMessages(ctx, relay, userID, &position); err != nil {
			return
		}
	}

	// 读帧会一直阻塞，单独用 goroutine 转发到通道，主循环才能同时等待唤醒信号与心跳。
	frames := make(chan clientFrame)
	go func() {
		for {
			var frame clientFrame
			if err := wsjson.Read(ctx, connection, &frame); err != nil {
				cancel()
				return
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()

	// 主循环串行处理三类事件：该用户有新消息时的唤醒推送、定时心跳保活、客户端发来的命令帧。
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wakeSignal:
			if err := s.pushChatMessages(ctx, relay, userID, &position); err != nil {
				return
			}
		case <-heartbeat.C:
			pingContext, cancelPing := context.WithTimeout(ctx, 10*time.Second)
			err := connection.Ping(pingContext)
			cancelPing()
			if err != nil {
				return
			}
			if authenticated {
				if err := s.pushChatMessages(ctx, relay, userID, &position); err != nil {
					return
				}
			}
		case frame := <-frames:
			switch frame.Type {
			case "send_message":
				if !authenticated || frame.ClientMessageID == uuid.Nil ||
					strings.TrimSpace(frame.Text) == "" || utf8.RuneCountInString(frame.Text) > maxMessageCharacters {
					connection.Close(websocket.StatusPolicyViolation, "invalid send_message")
					return
				}
				if !s.saveUserMessage(ctx, relay, userID, frame) {
					return
				}
			case "load_history":
				if frame.RequestID == "" {
					connection.Close(websocket.StatusPolicyViolation, "invalid load_history")
					return
				}
				if !authenticated {
					if err := relay.send(ctx, requestFailedEvent{Type: "request_failed", RequestID: frame.RequestID, Code: requestFailedUnauthorized}); err != nil {
						return
					}
					continue
				}
				before := uuid.Max
				if frame.Before != nil {
					before = *frame.Before
				}
				page, err := s.historyPage(ctx, userID, before)
				if err != nil {
					slog.Error("load history failed", "user_id", userID, "error", err)
					if err := relay.send(ctx, requestFailedEvent{Type: "request_failed", RequestID: frame.RequestID, Code: requestFailedInternal}); err != nil {
						return
					}
					continue
				}
				if err := relay.send(ctx, historyEvent{Type: "history", RequestID: frame.RequestID, chatMessages: page}); err != nil {
					return
				}
			case "request_code", "verify_code", "dev_login":
				if frame.RequestID == "" {
					connection.Close(websocket.StatusPolicyViolation, "invalid login request")
					return
				}
				if authenticated {
					if err := relay.send(ctx, requestFailedEvent{Type: "request_failed", RequestID: frame.RequestID, Code: requestFailedAlreadyAuthenticated}); err != nil {
						return
					}
					continue
				}
				if !s.handleLoginFrame(ctx, relay, frame, bindSession, &position) {
					return
				}
			case "logout":
				if frame.RequestID == "" {
					connection.Close(websocket.StatusPolicyViolation, "invalid logout")
					return
				}
				if !authenticated {
					if err := relay.send(ctx, requestFailedEvent{Type: "request_failed", RequestID: frame.RequestID, Code: requestFailedUnauthorized}); err != nil {
						return
					}
					continue
				}
				if err := s.queries.DeleteSession(ctx, sessionID); err != nil {
					slog.Error("delete session failed", "error", err)
					if err := relay.send(ctx, requestFailedEvent{Type: "request_failed", RequestID: frame.RequestID, Code: requestFailedInternal}); err != nil {
						return
					}
					continue
				}
				unbindSession()
				if err := relay.send(ctx, loggedOutEvent{Type: "logged_out", RequestID: frame.RequestID}); err != nil {
					return
				}
			default:
				connection.Close(websocket.StatusPolicyViolation, "unknown frame type")
				return
			}
		}
	}
}

// handleLoginFrame 处理未登录连接上的登录命令；登录成功后把连接绑定到该用户并回 authenticated 事件。
// 返回 false 表示连接已不可用，调用方应退出主循环。
func (s *server) handleLoginFrame(ctx context.Context, relay *eventRelay, frame clientFrame, bindSession func(database.GetActiveSessionRow), position *uuid.UUID) bool {
	// fail 把失败码按本次请求的 request_id 回给前端，并以“事件是否发得出”作为连接是否可用的判断。
	fail := func(code string) bool {
		return relay.send(ctx, requestFailedEvent{Type: "request_failed", RequestID: frame.RequestID, Code: code}) == nil
	}

	// 三种命令的区别只在如何拿到令牌：request_code 只发码不登录、回执后即结束，另两种拿到令牌后走下面的统一绑定。
	var token string
	switch frame.Type {
	case "request_code":
		if code := s.requestLoginCodeByEmail(ctx, frame.Email); code != "" {
			return fail(code)
		}
		return relay.send(ctx, codeSentEvent{Type: "code_sent", RequestID: frame.RequestID}) == nil
	case "dev_login":
		if s.appConfig.AppEnv != "dev" {
			return fail(requestFailedDevLoginUnavailable)
		}
		var err error
		token, err = s.createUserSession(ctx, devUserEmail)
		if err != nil {
			slog.Error("create dev session failed", "error", err)
			return fail(requestFailedInternal)
		}
	case "verify_code":
		var code string
		token, code = s.verifyLoginCodeByEmail(ctx, frame.Email, frame.Code)
		if code != "" {
			return fail(code)
		}
	}

	// 绑定时把推送位置定在最新消息处，首屏历史随 authenticated 事件下发，避免之后的主循环推送重复带上旧消息。
	session, err := s.findSession(ctx, token)
	if err != nil {
		slog.Error("find new session failed", "error", err)
		return fail(requestFailedInternal)
	}
	bindSession(session)
	if latest, err := s.queries.GetLatestMainMessageID(ctx, session.UserID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("get latest message failed", "error", err)
		return false
	} else if err == nil {
		*position = latest
	}
	history, err := s.historyPage(ctx, session.UserID, uuid.Max)
	if err != nil {
		slog.Error("load initial history failed", "error", err)
		return false
	}
	return relay.send(ctx, authenticatedEvent{
		Type: "authenticated", RequestID: frame.RequestID, Token: token, Email: session.Email, History: history,
	}) == nil
}

// saveUserMessage 在一个事务里落库用户消息并发出数据库通知，使该用户的其他连接也能被唤醒推送；成功后唤醒 agent 并回 delivered 确认。
// 返回 false 表示连接已不可用，调用方应退出主循环。
func (s *server) saveUserMessage(ctx context.Context, relay *eventRelay, userID uuid.UUID, frame clientFrame) bool {
	err := pgx.BeginFunc(ctx, s.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if err := queries.LockUserMessages(ctx, userID); err != nil {
			return err
		}
		if _, err := queries.SyncRecapState(ctx, userID); err != nil {
			return err
		}
		if _, err := queries.InsertUserMessage(ctx, database.InsertUserMessageParams{
			UserID:          userID,
			Text:            frame.Text,
			ClientMessageID: &frame.ClientMessageID,
		}); err != nil {
			return err
		}
		if _, err := queries.SyncRecapState(ctx, userID); err != nil {
			return err
		}
		return queries.NotifyUserMessages(ctx, userID)
	})
	if err != nil {
		slog.Error("save user message failed", "user_id", userID, "error", err)
		relay.connection.Close(websocket.StatusInternalError, "internal server error")
		return false
	}

	s.runner.Wake(userID)
	return relay.send(ctx, deliveredEvent{Type: "delivered", ClientMessageID: frame.ClientMessageID}) == nil
}

// pushChatMessages 把 position 之后的新消息一次性推给连接，并推进 position 作为下次推送的断点；同一事件还附带已读位置与 agent 工作状态，供前端一次更新列表与状态指示。
func (s *server) pushChatMessages(ctx context.Context, relay *eventRelay, userID uuid.UUID, position *uuid.UUID) error {
	// 单页查询上限 100 行，循环取到不满页为止，避免积压较多时漏掉后面的消息。
	var rows []database.ListChatRowsAfterRow
	for {
		page, err := s.queries.ListChatRowsAfter(ctx, database.ListChatRowsAfterParams{UserID: userID, ID: *position})
		if err != nil {
			slog.Error("list chat rows failed", "user_id", userID, "error", err)
			return err
		}
		rows = append(rows, page...)
		if len(page) > 0 {
			*position = page[len(page)-1].ID
		}
		if len(page) < 100 {
			break
		}
	}

	readUpTo, err := s.readUpTo(ctx, userID)
	if err != nil {
		slog.Error("get read position failed", "user_id", userID, "error", err)
		return err
	}
	mainThinking, err := s.queries.IsMainThinking(ctx, userID)
	if err != nil {
		slog.Error("get Main activity failed", "user_id", userID, "error", err)
		return err
	}
	activity := chatActivity{State: "idle"}
	recap, err := s.queries.GetRecapState(ctx, userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		activity.Revision, activity.WorkVersion = recap.Revision, recap.WorkVersion
		if recap.Active != nil && *recap.Active {
			activity.State, activity.Summary = "working", recap.Summary
		}
	}
	return relay.send(ctx, messagesEvent{
		Type: "messages", MainThinking: mainThinking, Activity: activity,
		chatMessages: chatMessages{Messages: chatMessagesFromRows(rows), ReadUpTo: readUpTo},
	})
}
