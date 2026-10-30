// 本文件是 httpapi 包的总入口：创建 server 并注册健康检查、管理与 WebSocket 等全部 HTTP 路由。

package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

type server struct {
	databasePool *pgxpool.Pool
	queries      *database.Queries
	appConfig    config.Config
	httpClient   *http.Client
	runner       *agent.Runner
	connections  *chatConnections
	services     *services.Store
}

func NewHandler(databasePool *pgxpool.Pool, appConfig config.Config, runner *agent.Runner, serviceSettings *services.Store) http.Handler {
	s := &server{
		databasePool: databasePool,
		queries:      database.New(databasePool),
		appConfig:    appConfig,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		runner:       runner,
		connections:  newChatConnections(),
		services:     serviceSettings,
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
	mux.Handle("GET /admin/timeline", s.requireAdmin(http.HandlerFunc(s.adminTimeline)))
	mux.Handle("GET /admin/timeline/messages/{id}", s.requireAdmin(http.HandlerFunc(s.adminTimelineMessage)))
	mux.Handle("GET /admin/models", s.requireAdmin(http.HandlerFunc(s.adminModels)))
	mux.Handle("GET /admin/model-settings", s.requireAdmin(http.HandlerFunc(s.adminModelSettings)))
	mux.Handle("PUT /admin/model-settings", s.requireAdmin(http.HandlerFunc(s.updateAdminModelSettings)))
	mux.Handle("GET /admin/services", s.requireAdmin(http.HandlerFunc(s.adminServices)))
	mux.Handle("PUT /admin/services/{service}", s.requireAdmin(http.HandlerFunc(s.updateAdminService)))
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
