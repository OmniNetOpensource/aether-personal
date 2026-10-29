// 本文件是 httpapi 包的总入口：创建 server 并注册健康检查、管理与 WebSocket 等全部 HTTP 路由。

package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

type server struct {
	databasePool *pgxpool.Pool
	queries      *database.Queries
	appConfig    config.Config
	httpClient   *http.Client
	runner       *agent.Runner
	connections  *chatConnections
}

func NewHandler(databasePool *pgxpool.Pool, appConfig config.Config, runner *agent.Runner) http.Handler {
	s := &server{
		databasePool: databasePool,
		queries:      database.New(databasePool),
		appConfig:    appConfig,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		runner:       runner,
		connections:  newChatConnections(),
	}
	go s.listenForUserMessages()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /admin/login", s.adminLogin)
	mux.Handle("POST /admin/logout", s.requireAdmin(http.HandlerFunc(s.adminLogout)))
	mux.Handle("GET /admin/me", s.requireAdmin(http.HandlerFunc(s.adminMe)))
	mux.Handle("GET /admin/snapshot", s.requireAdmin(http.HandlerFunc(s.adminSnapshot)))
	mux.Handle("GET /admin/calls", s.requireAdmin(http.HandlerFunc(s.adminCalls)))
	mux.Handle("GET /admin/calls/{id}", s.requireAdmin(http.HandlerFunc(s.adminCallDetail)))
	mux.Handle("GET /admin/models", s.requireAdmin(http.HandlerFunc(s.adminModels)))
	mux.Handle("GET /admin/model-settings", s.requireAdmin(http.HandlerFunc(s.adminModelSettings)))
	mux.Handle("PUT /admin/model-settings", s.requireAdmin(http.HandlerFunc(s.updateAdminModelSettings)))
	mux.HandleFunc("GET /ws", s.chatWebSocket)
	if appConfig.FrontendDir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(appConfig.FrontendDir)))
	}
	return mux
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.databasePool.Ping(r.Context()); err != nil {
		slog.Error("health check failed", "error", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}
