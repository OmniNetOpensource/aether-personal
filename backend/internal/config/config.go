package config

import (
	"fmt"
	"os"
)

type Config struct {
	AppEnv      string
	Port        string
	DatabaseURL string
}

func Load() (Config, error) {
	appEnv := os.Getenv("APP_ENV")
	if appEnv != "dev" && appEnv != "prod" {
		return Config{}, fmt.Errorf("APP_ENV must be dev or prod, got %q", appEnv)
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	return Config{
		AppEnv:      appEnv,
		Port:        port,
		DatabaseURL: databaseURL,
	}, nil
}
