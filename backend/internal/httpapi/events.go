// 本文件是服务器实时推送事件的唯一定义处：集中定义 WebSocket 下行事件类型，并由 eventRelay 统一写出，与前端 serverEvents.ts 一一对应。

package httpapi

import (
	"context"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// 后端通过聊天 WebSocket 发给前端的全部事件类型集中定义在本文件。
// websocket.go 只产生这些事件，不直接写 WebSocket；eventRelay 是唯一的写出口，
// 新增服务器推送事件时先在这里定义结构，再到前端 serverEvents.ts 补上对应定义。

// readyEvent 在已认证连接的 hello 握手完成后发出，确认订阅生效并返回当前同步游标。
type readyEvent struct {
	ChangeCursor int64  `json:"change_cursor,string"`
	Type         string `json:"type"`
}

// messagesEvent 推送增量可见消息，并携带 Main 处理与失败状态。
type messagesEvent struct {
	ChangeCursor int64  `json:"change_cursor,string"`
	Type         string `json:"type"`
	MainThinking bool   `json:"main_thinking"`
	MainFailed   bool   `json:"main_failed"`
	chatMessages
}

// eventRelay 中转后端发往单个前端连接的全部推送事件，并保证同一连接不会并发写。
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
