// 本文件是 httpapi 包的总入口：创建 server 并注册健康检查、管理与 WebSocket 等全部 HTTP 路由。

package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/sandbox"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// server 共享请求处理所需的连接池、Redis 客户端、服务配置与图片存储，图片入口经 Redis 跨实例共享缓存。
type server struct {
	databasePool *pgxpool.Pool
	redisClient  *redis.Client
	queries      *database.Queries
	appConfig    config.Config
	httpClient   *http.Client
	connections  *chatConnections
	services     *services.Store
	agents       *agent.Manager
	images       *images.Store
	sandbox      *sandbox.Store
}

// NewHandler 装配聊天与管理接口；聊天输入落库后经 Redis 跨实例唤醒 Agent 与聊天推送，订阅随进程 ctx 退出。
func NewHandler(ctx context.Context, databasePool *pgxpool.Pool, redisClient *redis.Client, appConfig config.Config, serviceSettings *services.Store, agents *agent.Manager, imageStore *images.Store) http.Handler {
	s := &server{
		databasePool: databasePool,
		redisClient:  redisClient,
		queries:      database.New(databasePool),
		appConfig:    appConfig,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		connections:  newChatConnections(),
		services:     serviceSettings,
		agents:       agents,
		images:       imageStore,
		sandbox:      sandbox.New(databasePool, serviceSettings, imageStore, appConfig.Sandbox),
	}
	go s.listenForUserMessages(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /admin/login", s.adminLogin)
	mux.Handle("POST /admin/logout", s.requireAdmin(http.HandlerFunc(s.adminLogout)))
	mux.Handle("GET /admin/me", s.requireAdmin(http.HandlerFunc(s.adminMe)))
	mux.Handle("GET /admin/snapshot", s.requireAdmin(http.HandlerFunc(s.adminSnapshot)))
	mux.Handle("GET /admin/calls/{id}", s.requireAdmin(http.HandlerFunc(s.adminCallDetail)))
	mux.Handle("GET /admin/timeline", s.requireAdmin(http.HandlerFunc(s.adminTimeline)))
	mux.Handle("GET /admin/timeline/messages/{id}", s.requireAdmin(http.HandlerFunc(s.adminTimelineMessage)))
	mux.Handle("GET /admin/models", s.requireAdmin(http.HandlerFunc(s.adminModels)))
	mux.Handle("GET /admin/model-settings", s.requireAdmin(http.HandlerFunc(s.adminModelSettings)))
	mux.Handle("PUT /admin/model-settings", s.requireAdmin(http.HandlerFunc(s.updateAdminModelSettings)))
	mux.Handle("GET /admin/quotas", s.requireAdmin(http.HandlerFunc(s.adminQuotas)))
	mux.Handle("GET /admin/services", s.requireAdmin(http.HandlerFunc(s.adminServices)))
	mux.Handle("PUT /admin/services/{service}", s.requireAdmin(http.HandlerFunc(s.updateAdminService)))
	mux.HandleFunc("GET /ws", s.chatWebSocket)
	mux.HandleFunc("GET /sandbox/desktop", s.sandboxDesktop)
	mux.HandleFunc("POST /images", s.uploadImage)
	mux.HandleFunc("POST /files", s.uploadFile)
	if appConfig.FrontendDir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(appConfig.FrontendDir)))
	}
	return mux
}

// healthz 同时探测 PostgreSQL 与 Redis 连通性，供网关主动健康检查在单实例异常时自动切流。
func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.databasePool.Ping(r.Context()); err != nil {
		slog.Error("database health check failed", "error", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.redisClient.Ping(r.Context()).Err(); err != nil {
		slog.Error("redis health check failed", "error", err)
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}
