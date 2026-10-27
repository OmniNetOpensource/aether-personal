package httpapi

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/sandbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

type server struct {
	databasePool   *pgxpool.Pool
	queries        *database.Queries
	appConfig      config.Config
	httpClient     *http.Client
	runner         *agent.Runner
	connections    *chatConnections
	sandboxClient  sandbox.Provider
	desktopViewers sync.Map
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
	if config.SandboxEnabled && appConfig.SandboxURL != "" {
		s.sandboxClient = sandbox.NewProvider(appConfig.SandboxProvider, appConfig.SandboxURL, appConfig.SandboxAPIKey, appConfig.SandboxDomain, nil)
	}
	go s.listenForUserMessages()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /auth/config", s.authConfig)
	if appConfig.AppEnv == "dev" {
		mux.HandleFunc("POST /auth/dev", s.devLogin)
	}
	mux.HandleFunc("POST /auth/code", s.requestLoginCode)
	mux.HandleFunc("POST /auth/verify", s.verifyLoginCode)
	mux.Handle("POST /auth/logout", s.requireSession(http.HandlerFunc(s.logout)))
	mux.Handle("GET /messages", s.requireSession(http.HandlerFunc(s.listMessages)))
	if config.SandboxEnabled {
		mux.Handle("GET /sandbox", s.requireSession(http.HandlerFunc(s.sandboxStatus)))
		mux.HandleFunc("GET /sandbox/desktop", s.sandboxDesktop)
	} else {
		mux.HandleFunc("GET /sandbox", http.NotFound)
		mux.HandleFunc("GET /sandbox/", http.NotFound)
	}
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
