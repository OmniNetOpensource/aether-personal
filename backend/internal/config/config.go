package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	AppEnv           string
	Port             string
	DatabaseURL      string
	DatabaseMaxConns int32
	ResendAPIKey     string
	EmailFrom        string
	OpenRouterAPIKey string
	MainModel        string
	SubagentModel    string
	SERPAPIKey       string
	ParallelAPIKey   string
	EveryInfraAPIKey string
	FrontendDir      string
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

	databaseMaxConns := int32(20)
	if value := os.Getenv("DATABASE_MAX_CONNS"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil || parsed < 5 {
			return Config{}, fmt.Errorf("DATABASE_MAX_CONNS must be at least 5, got %q", value)
		}
		databaseMaxConns = int32(parsed)
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

	openRouterAPIKey := os.Getenv("OPENROUTER_API_KEY")
	if openRouterAPIKey == "" {
		return Config{}, fmt.Errorf("OPENROUTER_API_KEY is required")
	}

	mainModel := os.Getenv("MAIN_MODEL")
	if mainModel == "" {
		mainModel = "minimax/minimax-m3.1-flash"
	}
	subagentModel := os.Getenv("SUBAGENT_MODEL")
	if subagentModel == "" {
		subagentModel = mainModel
	}

	return Config{
		AppEnv:           appEnv,
		Port:             port,
		DatabaseURL:      databaseURL,
		DatabaseMaxConns: databaseMaxConns,
		ResendAPIKey:     resendAPIKey,
		EmailFrom:        emailFrom,
		OpenRouterAPIKey: openRouterAPIKey,
		MainModel:        mainModel,
		SubagentModel:    subagentModel,
		SERPAPIKey:       os.Getenv("SERP_API_KEY"),
		ParallelAPIKey:   os.Getenv("PARALLEL_API_KEY"),
		EveryInfraAPIKey: os.Getenv("EVERYINFRA_API_KEY"),
		FrontendDir:      os.Getenv("FRONTEND_DIR"),
	}, nil
}
