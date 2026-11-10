// 本文件读取 FC 的真实状态并打开已有桌面流；观察路径不创建、恢复或续期实例。

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	e2b "github.com/aliyun-fc/e2b-go-sdk"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Desktop 只公开观察到的状态和实例标识；流地址与解密后的凭证仅留在后端。
type Desktop struct {
	State        string
	ID           string
	streamURL    string
	trafficToken string
}

// Desktop 查询已有绑定和控制面；缺少流能力的旧实例等待维护者更新，不自动替换用户电脑。
func (s *Store) Desktop(ctx context.Context, userID uuid.UUID) (Desktop, error) {
	workspace, err := s.queries.GetSandboxWorkspace(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Desktop{State: "unconfigured"}, nil
	}
	if err != nil {
		return Desktop{}, err
	}
	if workspace.SandboxID == nil {
		return Desktop{State: "unallocated"}, nil
	}
	if err := s.config.Validate(); err != nil {
		return Desktop{}, err
	}
	client, err := s.client(ctx)
	if err != nil {
		return Desktop{}, err
	}
	info, err := client.GetSandboxInfo(ctx, *workspace.SandboxID)
	var missing *e2b.NotFoundError
	if errors.As(err, &missing) {
		return Desktop{State: "missing"}, nil
	}
	if err != nil {
		return Desktop{}, err
	}
	desktop := Desktop{State: string(info.State), ID: info.SandboxID}
	if desktop.State != "running" {
		return desktop, nil
	}
	// vnc1 由关闭流量自动唤醒的新实例写入；旧实例只能由维护者显式迁移。
	if info.Metadata["aether.desktop_stream"] != "vnc1" || len(workspace.TrafficTokenCiphertext) == 0 {
		desktop.State = "needs_update"
		return desktop, nil
	}
	desktop.trafficToken, err = s.services.DecryptSecret("sandbox-traffic:"+userID.String(), workspace.TrafficTokenCiphertext)
	if err != nil {
		return Desktop{}, err
	}
	domain := info.SandboxDomain
	if domain == "" {
		domain = s.config.Region + ".e2b.fc.aliyuncs.com"
	}
	desktop.streamURL = fmt.Sprintf("wss://18092-%s.%s/", desktop.ID, domain)
	return desktop, nil
}

// Open 只建立受 FC 流量鉴权保护的 WebSocket；VNC 端口与令牌不交给浏览器。
func (d Desktop) Open(ctx context.Context) (net.Conn, error) {
	dialContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(dialContext, d.streamURL, &websocket.DialOptions{
		HTTPClient:   &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		HTTPHeader:   http.Header{"E2b-Traffic-Access-Token": {d.trafficToken}},
		Subprotocols: []string{"binary"},
	})
	if err != nil {
		if response != nil && response.Body != nil {
			err = errors.Join(err, response.Body.Close())
		}
		return nil, err
	}
	stream := websocket.NetConn(ctx, connection, websocket.MessageBinary)
	connection.SetReadLimit(16 << 20)
	return stream, nil
}
