package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

type server struct {
	databasePool *pgxpool.Pool
	queries      *database.Queries
	appConfig    config.Config
	httpClient   *http.Client
}

func NewHandler(databasePool *pgxpool.Pool, appConfig config.Config) http.Handler {
	s := &server{
		databasePool: databasePool,
		queries:      database.New(databasePool),
		appConfig:    appConfig,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /auth/code", s.requestLoginCode)
	mux.HandleFunc("POST /auth/verify", s.verifyLoginCode)
	mux.Handle("POST /auth/logout", s.requireSession(http.HandlerFunc(s.logout)))
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
