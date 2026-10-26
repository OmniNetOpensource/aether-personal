package sandbox

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func (c *Client) OpenDesktop(ctx context.Context, id string) (net.Conn, error) {
	endpoint := strings.Replace(c.baseURL, "http", "ws", 1) + sandboxPath(id) + "/proxy/6080/websockify"
	client := *c.httpClient
	client.Timeout = 0
	dialContext, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(dialContext, endpoint, &websocket.DialOptions{
		HTTPClient:   &client,
		HTTPHeader:   http.Header{"Open-Sandbox-Api-Key": {c.apiKey}},
		Subprotocols: []string{"binary"},
	})
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, err
	}
	return websocket.NetConn(ctx, connection, websocket.MessageBinary), nil
}
