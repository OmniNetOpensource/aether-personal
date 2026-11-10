// 本文件把登录用户连接到自己的只读桌面；认证先于云端访问，暂停或重建时主动断流。

package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"
)

// errDesktopChanged 表示正常的生命周期切换，前端重新读取状态即可，不属于服务故障。
var errDesktopChanged = errors.New("desktop state changed")

// sandboxDesktop 使用首帧认证，避免把登录令牌写入 URL；后续只转发 RFB 显示协议。
func (s *server) sandboxDesktop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer connection.CloseNow()
	connection.SetReadLimit(8 << 10)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	var hello struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	authContext, stopAuth := context.WithTimeout(ctx, 5*time.Second)
	err = wsjson.Read(authContext, connection, &hello)
	stopAuth()
	if err != nil || hello.Type != "hello" || hello.Token == "" || len(hello.Token) > 128 {
		connection.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}
	session, err := s.findSession(ctx, hello.Token)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("authenticate desktop viewer", "error", err)
		}
		connection.Close(websocket.StatusPolicyViolation, "unauthorized")
		return
	}
	// 只有最外层记录故障并显示固定错误，供应商地址和凭证不进入客户端消息。
	fail := func(err error) {
		status := websocket.CloseStatus(err)
		if ctx.Err() != nil || status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
			connection.Close(websocket.StatusNormalClosure, "desktop disconnected")
			return
		}
		slog.Error("desktop stream failed", "user_id", session.UserID, "error", err)
		connection.Close(websocket.StatusTryAgainLater, "desktop unavailable")
	}
	stateContext, stopState := context.WithTimeout(ctx, 10*time.Second)
	desktop, err := s.sandbox.Desktop(stateContext, session.UserID)
	stopState()
	if err != nil {
		fail(err)
		return
	}
	if desktop.State != "running" {
		if err := wsjson.Write(ctx, connection, map[string]string{"type": "state", "state": desktop.State}); err != nil {
			return
		}
		connection.Close(websocket.StatusNormalClosure, "desktop is not running")
		return
	}

	// 三个并发方向共用取消上下文；任一个结束都会停止其他方向，Wait 收回所有 goroutine。
	group, streamContext := errgroup.WithContext(ctx)
	provider, err := desktop.Open(streamContext)
	if err != nil {
		fail(err)
		return
	}
	defer provider.Close()
	if err := wsjson.Write(ctx, connection, map[string]string{"type": "ready"}); err != nil {
		return
	}
	viewer := websocket.NetConn(streamContext, connection, websocket.MessageBinary)
	connection.SetReadLimit(64 << 10)
	defer viewer.Close()
	if err := errors.Join(provider.SetDeadline(time.Now().Add(10*time.Second)), viewer.SetDeadline(time.Now().Add(10*time.Second))); err != nil {
		fail(err)
		return
	}
	if err := relayDesktopHandshake(provider, viewer); err != nil {
		fail(err)
		return
	}
	if err := errors.Join(provider.SetDeadline(time.Time{}), viewer.SetDeadline(time.Time{})); err != nil {
		fail(err)
		return
	}
	group.Go(func() error { return relayDesktopRequests(provider, viewer) })
	group.Go(func() error {
		_, err := io.Copy(viewer, provider)
		if err == nil {
			return io.EOF
		}
		return err
	})
	// 会话注销、实例暂停或实例 ID 更换均终止旧流；查询路径不会恢复或续期实例。
	group.Go(func() error {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		tokenHash := sha256.Sum256([]byte(hello.Token))
		for {
			select {
			case <-streamContext.Done():
				return streamContext.Err()
			case <-ticker.C:
				checkContext, stopCheck := context.WithTimeout(streamContext, 8*time.Second)
				_, err := s.queries.GetActiveSession(checkContext, tokenHash[:])
				if errors.Is(err, pgx.ErrNoRows) {
					stopCheck()
					return errDesktopChanged
				}
				if err != nil {
					stopCheck()
					return err
				}
				current, err := s.sandbox.Desktop(checkContext, session.UserID)
				stopCheck()
				if err != nil {
					return err
				}
				if current.State != "running" || current.ID != desktop.ID {
					return errDesktopChanged
				}
			}
		}
	})
	err = group.Wait()
	status := websocket.CloseStatus(err)
	if errors.Is(err, errDesktopChanged) || errors.Is(err, io.EOF) || ctx.Err() != nil || errors.Is(err, net.ErrClosed) || status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
		connection.Close(websocket.StatusNormalClosure, "desktop disconnected")
		return
	}
	fail(err)
}
