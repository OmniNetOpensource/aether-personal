// 本文件负责启动配置：只从环境变量读取并校验配置，prod 缺少必需项时让程序在启动阶段直接失败。

package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const DefaultMaxOutputTokens = 8192

type Config struct {
	MaxOutputTokens     int32
	AdminPasswordSalt   []byte
	AdminPasswordKey    []byte
	AppEnv              string
	Port                string
	DatabaseURL         string
	DatabaseMaxConns    int32
	ResendAPIKey        string
	EmailFrom           string
	OpenRouterAPIKey    string
	MainModel           string
	RecapModel          string
	SubagentModel       string
	SubagentFollowsMain bool
	SERPAPIKey          string
	ParallelAPIKey      string
	EveryInfraAPIKey    string
	FrontendDir         string
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
		mainModel = "deepseek/deepseek-v4.1-flash-1017"
	}
	subagentModel := os.Getenv("SUBAGENT_MODEL")
	if subagentModel == "" {
		subagentModel = mainModel
	}

	maxOutputTokens := int32(DefaultMaxOutputTokens)
	if value := os.Getenv("MAX_OUTPUT_TOKENS"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil || parsed < 1 {
			return Config{}, fmt.Errorf("MAX_OUTPUT_TOKENS must be a positive integer")
		}
		maxOutputTokens = int32(parsed)
	}
	var adminPasswordSalt, adminPasswordKey []byte
	if value := os.Getenv("ADMIN_PASSWORD_HASH"); value != "" {
		parts := strings.Split(value, "$")
		if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "600000" {
			return Config{}, fmt.Errorf("invalid ADMIN_PASSWORD_HASH; use scripts/set-admin-password.py")
		}
		var err error
		adminPasswordSalt, err = base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil || len(adminPasswordSalt) != 16 {
			return Config{}, fmt.Errorf("invalid ADMIN_PASSWORD_HASH salt")
		}
		adminPasswordKey, err = base64.RawURLEncoding.DecodeString(parts[3])
		if err != nil || len(adminPasswordKey) != 32 {
			return Config{}, fmt.Errorf("invalid ADMIN_PASSWORD_HASH key")
		}
	}

	return Config{
		AppEnv:              appEnv,
		MaxOutputTokens:     maxOutputTokens,
		AdminPasswordSalt:   adminPasswordSalt,
		AdminPasswordKey:    adminPasswordKey,
		Port:                port,
		DatabaseURL:         databaseURL,
		DatabaseMaxConns:    databaseMaxConns,
		ResendAPIKey:        resendAPIKey,
		EmailFrom:           emailFrom,
		OpenRouterAPIKey:    openRouterAPIKey,
		MainModel:           mainModel,
		RecapModel:          os.Getenv("RECAP_MODEL"),
		SubagentModel:       subagentModel,
		SubagentFollowsMain: os.Getenv("SUBAGENT_MODEL") == "",
		SERPAPIKey:          os.Getenv("SERP_API_KEY"),
		ParallelAPIKey:      os.Getenv("PARALLEL_API_KEY"),
		EveryInfraAPIKey:    os.Getenv("EVERYINFRA_API_KEY"),
		FrontendDir:         os.Getenv("FRONTEND_DIR"),
	}, nil
}
