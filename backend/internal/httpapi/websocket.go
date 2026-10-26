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

const maxMessageCharacters = 32000

type chatActivity struct {
	State       string `json:"state"`
	Summary     string `json:"summary"`
	Revision    int64  `json:"revision"`
	WorkVersion int64  `json:"work_version"`
}

// chatConnections 记录本机每个用户的 WebSocket 连接；收到该用户的 NOTIFY 时唤醒这些连接去查表推送。
type chatConnections struct {
	mutex       sync.Mutex
	wakeSignals map[uuid.UUID]map[chan struct{}]struct{}
}

func newChatConnections() *chatConnections {
	return &chatConnections{wakeSignals: map[uuid.UUID]map[chan struct{}]struct{}{}}
}

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

func (c *chatConnections) remove(userID uuid.UUID, wakeSignal chan struct{}) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	delete(c.wakeSignals[userID], wakeSignal)
	if len(c.wakeSignals[userID]) == 0 {
		delete(c.wakeSignals, userID)
	}
}

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

func (s *server) listenForUserMessages() {
	for {
		err := s.listenOnce(context.Background())
		slog.Error("listen for user messages stopped", "error", err)
		time.Sleep(time.Second)
	}
}

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

func (s *server) chatWebSocket(w http.ResponseWriter, r *http.Request) {
	// 登录靠第一条消息中的令牌而非 Cookie，跨站页面无法冒用身份，因此不校验 Origin（Flutter Web 可能部署在其他域名）。
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	// 32,000 个字符的 UTF-8 编码最多约 128 KB。
	connection.SetReadLimit(256 << 10)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var auth struct {
		Type         string     `json:"type"`
		Token        string     `json:"token"`
		LastPosition *uuid.UUID `json:"last_position"`
	}
	authContext, cancelAuth := context.WithTimeout(ctx, 5*time.Second)
	err = wsjson.Read(authContext, connection, &auth)
	cancelAuth()
	if err != nil || auth.Type != "auth" {
		connection.Close(websocket.StatusPolicyViolation, "auth required")
		return
	}
	session, err := s.findSession(ctx, auth.Token)
	if errors.Is(err, pgx.ErrNoRows) {
		connection.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}
	if err != nil {
		slog.Error("find session failed", "error", err)
		connection.Close(websocket.StatusInternalError, "internal server error")
		return
	}
	userID := session.UserID

	var position uuid.UUID
	if auth.LastPosition != nil {
		position = *auth.LastPosition
	} else {
		position, err = s.queries.GetLatestMainMessageID(ctx, userID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("get latest message failed", "error", err)
			connection.Close(websocket.StatusInternalError, "internal server error")
			return
		}
	}

	wakeSignal := s.connections.add(userID)
	defer s.connections.remove(userID, wakeSignal)

	go func() {
		defer cancel()
		s.receiveChatMessages(ctx, connection, userID)
	}()

	if err := s.pushChatMessages(ctx, connection, userID, &position); err != nil {
		return
	}
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wakeSignal:
			if err := s.pushChatMessages(ctx, connection, userID, &position); err != nil {
				return
			}
		case <-heartbeat.C:
			pingContext, cancelPing := context.WithTimeout(ctx, 10*time.Second)
			err := connection.Ping(pingContext)
			cancelPing()
			if err != nil {
				return
			}
			if err := s.pushChatMessages(ctx, connection, userID, &position); err != nil {
				return
			}
		}
	}
}

func (s *server) receiveChatMessages(ctx context.Context, connection *websocket.Conn, userID uuid.UUID) {
	for {
		var frame struct {
			Type            string    `json:"type"`
			ClientMessageID uuid.UUID `json:"client_message_id"`
			Text            string    `json:"text"`
		}
		if err := wsjson.Read(ctx, connection, &frame); err != nil {
			return
		}
		if frame.Type != "send_message" || frame.ClientMessageID == uuid.Nil ||
			strings.TrimSpace(frame.Text) == "" || utf8.RuneCountInString(frame.Text) > maxMessageCharacters {
			connection.Close(websocket.StatusPolicyViolation, "invalid send_message")
			return
		}

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
			connection.Close(websocket.StatusInternalError, "internal server error")
			return
		}

		s.runner.Wake(userID)
		if err := wsjson.Write(ctx, connection, map[string]any{"type": "delivered", "client_message_id": frame.ClientMessageID}); err != nil {
			return
		}
	}
}

func (s *server) pushChatMessages(ctx context.Context, connection *websocket.Conn, userID uuid.UUID, position *uuid.UUID) error {
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
	return wsjson.Write(ctx, connection, struct {
		Type         string       `json:"type"`
		MainThinking bool         `json:"main_thinking"`
		Activity     chatActivity `json:"activity"`
		chatMessages
	}{Type: "messages", MainThinking: mainThinking, Activity: activity, chatMessages: chatMessages{Messages: chatMessagesFromRows(rows), ReadUpTo: readUpTo}})
}
