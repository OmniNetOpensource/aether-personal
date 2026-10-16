package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
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

	databasePool, err := pgxpool.New(context.Background(), appConfig.DatabaseURL)
	if err != nil {
		slog.Error("create database pool failed", "error", err)
		os.Exit(1)
	}

	address := ":" + appConfig.Port
	slog.Info("server listening", "address", address, "app_env", appConfig.AppEnv)
	if err := http.ListenAndServe(address, httpapi.NewHandler(databasePool, appConfig)); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
