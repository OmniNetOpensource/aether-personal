// 本文件是服务器事件的唯一定义处：集中定义 WebSocket 下行事件类型，并由 eventRelay 统一写出，与前端 serverEvents.ts 一一对应。

package httpapi

import (
	"context"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

// 后端通过聊天 WebSocket 发给前端的全部事件类型集中定义在本文件。
// websocket.go 只产生这些事件，不直接写 WebSocket；eventRelay 是唯一的写出口，
// 新增服务器事件时先在这里定义结构，再到前端 serverEvents.ts 补上对应定义。

const (
	requestFailedInvalidEmail         = "invalid_email"
	requestFailedTooFrequent          = "too_frequent"
	requestFailedInvalidCode          = "invalid_code"
	requestFailedDevLoginUnavailable  = "dev_login_unavailable"
	requestFailedUnauthorized         = "unauthorized"
	requestFailedAlreadyAuthenticated = "already_authenticated"
	requestFailedInternal             = "internal"
	requestFailedVerificationRequired = "verification_required"
	requestFailedLoginUnavailable     = "login_unavailable"
	requestFailedInvalidRequest       = "invalid_request"
	requestFailedNotFound             = "not_found"
	requestFailedInvalidShare         = "invalid_share"
	requestFailedShareNotFound        = "share_not_found"
)

// readyEvent 是连接建立后后端发出的第一个事件：告知登录状态，
// 已登录时同时带上最新一页历史、桌面可用性与任务快照，前端收到后才结束 loading 态。
type readyEvent struct {
	ChangeCursor     int64         `json:"change_cursor,string"`
	Type             string        `json:"type"`
	Authenticated    bool          `json:"authenticated"`
	DevLogin         bool          `json:"dev_login"`
	DesktopAvailable bool          `json:"desktop_available,omitempty"`
	Email            string        `json:"email,omitempty"`
	History          *chatMessages `json:"history,omitempty"`
	Tasks            []chatTask    `json:"tasks,omitempty"`
	TurnstileSiteKey string        `json:"turnstile_site_key,omitempty"`
}

// authenticatedEvent 在同一条连接上完成登录（验证码或开发登录）后发出。
type authenticatedEvent struct {
	ChangeCursor     int64        `json:"change_cursor,string"`
	Type             string       `json:"type"`
	RequestID        string       `json:"request_id"`
	Token            string       `json:"token"`
	Email            string       `json:"email"`
	DesktopAvailable bool         `json:"desktop_available,omitempty"`
	History          chatMessages `json:"history"`
	Tasks            []chatTask   `json:"tasks"`
}

type codeSentEvent struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

type historyEvent struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	chatMessages
}

// searchResultsEvent 与正常历史分开传递，结果分页不会推进聊天的历史断点。
type searchResultsEvent struct {
	Type       string          `json:"type"`
	RequestID  string          `json:"request_id"`
	Results    []chatSearchHit `json:"results"`
	NextBefore *uuid.UUID      `json:"next_before,omitempty"`
}

// messageContextEvent 提供独立历史窗口；两侧是否还有记录由多取的一行判断。
type messageContextEvent struct {
	Type          string `json:"type"`
	RequestID     string `json:"request_id"`
	HasMoreBefore bool   `json:"has_more_before"`
	HasMoreAfter  bool   `json:"has_more_after"`
	chatMessages
}

// shareCreatedEvent 返回新生成的公开分享链接摘要，供前端复制链接并刷新管理列表。
type shareCreatedEvent struct {
	Type      string              `json:"type"`
	RequestID string              `json:"request_id"`
	Share     messageShareSummary `json:"share"`
}

// shareListEvent 返回当前用户创建过的全部公开分享记录。
type shareListEvent struct {
	Type      string                `json:"type"`
	RequestID string                `json:"request_id"`
	Shares    []messageShareSummary `json:"shares"`
}

// shareDeletedEvent 确认指定分享记录已撤销删除，原公开链接随即失效。
type shareDeletedEvent struct {
	Type      string    `json:"type"`
	RequestID string    `json:"request_id"`
	ShareID   uuid.UUID `json:"share_id"`
}

// messagesEvent 推送增量可见消息，并携带 Main 处理状态与当前任务队列快照。
type messagesEvent struct {
	ChangeCursor int64      `json:"change_cursor,string"`
	Type         string     `json:"type"`
	MainThinking bool       `json:"main_thinking"`
	MainFailed   bool       `json:"main_failed"`
	Tasks        []chatTask `json:"tasks"`
	chatMessages
}

type deliveredEvent struct {
	Type            string    `json:"type"`
	ClientMessageID uuid.UUID `json:"client_message_id"`
}

type loggedOutEvent struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

// requestFailedEvent 用于登录、翻页与搜索这类用户可重试的请求；
// send_message 不合规则仍按既有约定直接断开连接，不走这个事件。
type requestFailedEvent struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
}

// eventRelay 中转后端发往单个前端连接的全部事件，并保证同一连接不会并发写。
type eventRelay struct {
	connection *websocket.Conn
	writeMutex sync.Mutex
}

func newEventRelay(connection *websocket.Conn) *eventRelay {
	return &eventRelay{connection: connection}
}

func (r *eventRelay) send(ctx context.Context, event any) error {
	r.writeMutex.Lock()
	defer r.writeMutex.Unlock()
	return wsjson.Write(ctx, r.connection, event)
}
