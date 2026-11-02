// 本文件是后端进程入口：装配配置、数据库连接池、Agent 调度、定时任务与 HTTP 服务，并随退出信号优雅关闭，把各模块连成一个可运行的服务。

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/httpapi"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/scheduler"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	appConfig, err := config.Load()
	if err != nil {
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}

	if appConfig.AppEnv == "prod" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	}

	poolConfig, err := pgxpool.ParseConfig(appConfig.DatabaseURL)
	if err != nil {
		slog.Error("parse DATABASE_URL failed", "error", err)
		os.Exit(1)
	}
	poolConfig.MaxConns = appConfig.DatabaseMaxConns
	databasePool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		slog.Error("create database pool failed", "error", err)
		os.Exit(1)
	}

	defer databasePool.Close()
	// HTTP 与 Agent 共用无缓存的服务配置入口，数据库保留凭证的唯一运行时副本。
	serviceSettings, err := services.New(databasePool, appConfig.ServiceEncryptionKey, appConfig.AppEnv)
	if err != nil {
		slog.Error("initialize service encryption failed")
		os.Exit(1)
	}
	runner := agent.NewRunner(databasePool, appConfig, serviceSettings)
	go runner.Start(ctx)
	go scheduler.Run(ctx, databasePool, runner.Wake)

	address := ":" + appConfig.Port
	slog.Info("server listening", "address", address, "app_env", appConfig.AppEnv)
	server := &http.Server{Addr: address, Handler: httpapi.NewHandler(databasePool, appConfig, runner, serviceSettings), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
