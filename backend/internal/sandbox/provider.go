package sandbox

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Provider interface {
	Create(context.Context, uuid.UUID, uuid.UUID, string) (Environment, error)
	Find(context.Context, uuid.UUID) ([]Environment, error)
	Get(context.Context, string) (Environment, error)
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	Ping(context.Context, string) error
	StartCommand(context.Context, string, string, string, time.Duration) (string, error)
	Command(context.Context, string, string) (Command, error)
	Logs(context.Context, string, string) (string, error)
	StopCommand(context.Context, string, string) error
	ReadFile(context.Context, string, string) ([]byte, error)
	WriteFile(context.Context, string, string, []byte) error
	OpenDesktop(context.Context, string) (net.Conn, error)
}

func NewProvider(provider, baseURL, apiKey, domain string, httpClient *http.Client) Provider {
	client := NewClient(baseURL, apiKey, httpClient)
	if provider == "aliyun" {
		client.baseURL = strings.TrimRight(baseURL, "/")
		client.headerName = "X-API-Key"
		return &Alibaba{control: client, domain: domain}
	}
	return client
}
