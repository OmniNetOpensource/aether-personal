package config

import (
	"fmt"
	"os"
)

type Config struct {
	AppEnv       string
	Port         string
	DatabaseURL  string
	ResendAPIKey string
	EmailFrom    string
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

	resendAPIKey := os.Getenv("RESEND_API_KEY")
	emailFrom := os.Getenv("EMAIL_FROM")
	if appEnv == "prod" && (resendAPIKey == "" || emailFrom == "") {
		return Config{}, fmt.Errorf("RESEND_API_KEY and EMAIL_FROM are required in prod")
	}

	return Config{
		AppEnv:       appEnv,
		Port:         port,
		DatabaseURL:  databaseURL,
		ResendAPIKey: resendAPIKey,
		EmailFrom:    emailFrom,
	}, nil
}
