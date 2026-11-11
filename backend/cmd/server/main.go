// 本文件是后端进程入口：装配数据库、Redis 客户端、共享图片缓存、Agent 与调度器、HTTP 服务，并随退出信号等待后台任务和请求处理结束。

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
	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/scheduler"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
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
	if appConfig.RedisURL == "" {
		slog.Error("REDIS_URL is required")
		os.Exit(1)
	}
	redisOptions, err := redis.ParseURL(appConfig.RedisURL)
	if err != nil {
		slog.Error("parse REDIS_URL failed", "error", err)
		os.Exit(1)
	}
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()

	// HTTP 与 Agent 共用无缓存的服务配置入口，数据库保留凭证的唯一运行时副本。
	serviceSettings, err := services.New(databasePool, appConfig.ServiceEncryptionKey, appConfig.AppEnv)
	if err != nil {
		slog.Error("initialize service encryption failed")
		os.Exit(1)
	}
	// 上传、工具和模型请求通过 Redis 跨实例共享图片缓存；过期由 Redis TTL 自动回收，无需后台清理协程。
	imageStore := images.NewStore(serviceSettings, redisClient, appConfig.ImageCacheMaxBytes, appConfig.ImageCacheTTL)
	manager := agent.NewManager(databasePool, redisClient, appConfig, serviceSettings, imageStore)
	managerDone := make(chan error, 1)
	go func() {
		err := manager.Start(ctx)
		if err != nil {
			stop()
		}
		managerDone <- err
	}()
	go scheduler.Run(ctx, databasePool, appConfig, manager)

	address := ":" + appConfig.Port
	slog.Info("server listening", "address", address, "app_env", appConfig.AppEnv)
	server := &http.Server{Addr: address, Handler: httpapi.NewHandler(ctx, databasePool, redisClient, appConfig, serviceSettings, manager, imageStore), ReadHeaderTimeout: 10 * time.Second}
	httpDone := make(chan struct{})
	go func() {
		defer close(httpDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
	stop()
	<-httpDone
	if err := <-managerDone; err != nil {
		slog.Error("background workers stopped", "error", err)
		os.Exit(1)
	}
}
