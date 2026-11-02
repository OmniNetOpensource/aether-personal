// 本文件是本地联调时临时使用的聊天客户端：走 dev 登录发两条消息并打印收到的事件，验证完即删除。

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

// main 连接本地后端，依次发送命令行给出的两条消息，等各自的回复到达后退出。
func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws://localhost:8080/ws", nil)
	if err != nil {
		panic(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	connection.SetReadLimit(16 << 20)
	readEvent := func() map[string]any {
		var event map[string]any
		if err := wsjson.Read(ctx, connection, &event); err != nil {
			panic(err)
		}
		return event
	}
	if err := wsjson.Write(ctx, connection, map[string]any{"type": "hello"}); err != nil {
		panic(err)
	}
	fmt.Println("event:", readEvent()["type"])
	if err := wsjson.Write(ctx, connection, map[string]any{"type": "dev_login", "request_id": "verify-1"}); err != nil {
		panic(err)
	}
	for {
		event := readEvent()
		fmt.Println("event:", event["type"])
		if event["type"] == "authenticated" {
			break
		}
	}
	for index, text := range os.Args[1:] {
		clientID := uuid.New()
		if err := wsjson.Write(ctx, connection, map[string]any{"type": "send_message", "request_id": fmt.Sprintf("verify-%d", index+2), "client_message_id": clientID, "text": text}); err != nil {
			panic(err)
		}
		receivedMain := false
		for {
			event := readEvent()
			fmt.Println("event:", event["type"], "thinking:", event["main_thinking"])
			if event["type"] != "messages" {
				continue
			}
			if messages, ok := event["messages"].([]any); ok {
				for _, raw := range messages {
					if message, ok := raw.(map[string]any); ok {
						fmt.Printf("message sender=%v text=%.200v\n", message["sender"], message["text"])
						if message["sender"] == "main" {
							receivedMain = true
						}
					}
				}
			}
			if thinking, ok := event["main_thinking"].(bool); ok && !thinking && receivedMain {
				break
			}
		}
	}
}
