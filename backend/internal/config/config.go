package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const DefaultMaxOutputTokens = 8192

// Sandbox execution and desktop viewing are temporarily disabled for the web-only release.
const SandboxEnabled = false

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
	SandboxProvider     string
	SandboxDomain       string
	SandboxURL          string
	SandboxAPIKey       string
	SandboxImage        string
	SandboxIdleSeconds  int
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

	sandboxProvider, sandboxDomain, sandboxURL, sandboxAPIKey, sandboxImage := "", "", "", "", ""
	sandboxIdleSeconds := 300
	if SandboxEnabled {
		sandboxProvider = os.Getenv("SANDBOX_PROVIDER")
		if sandboxProvider == "" {
			sandboxProvider = "opensandbox"
		}
		if sandboxProvider != "opensandbox" && sandboxProvider != "aliyun" {
			return Config{}, fmt.Errorf("SANDBOX_PROVIDER must be opensandbox or aliyun")
		}
		sandboxDomain = os.Getenv("SANDBOX_DOMAIN")
		sandboxURL = os.Getenv("SANDBOX_URL")
		sandboxAPIKey = os.Getenv("SANDBOX_API_KEY")
		sandboxImage = os.Getenv("SANDBOX_IMAGE")
		if sandboxURL != "" {
			parsed, err := url.Parse(sandboxURL)
			if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
				return Config{}, fmt.Errorf("SANDBOX_URL must be an HTTP(S) origin without credentials, path, query or fragment")
			}
			if sandboxProvider == "aliyun" {
				if parsed.Scheme != "https" || !strings.HasSuffix(parsed.Hostname(), ".e2b.fc.aliyuncs.com") || parsed.Port() != "" || !strings.HasPrefix(parsed.Hostname(), "api.") || sandboxDomain != strings.TrimPrefix(parsed.Hostname(), "api.") {
					return Config{}, fmt.Errorf("aliyun requires its regional HTTPS API origin and matching SANDBOX_DOMAIN")
				}
			}
			if sandboxAPIKey == "" || sandboxImage == "" {
				return Config{}, fmt.Errorf("SANDBOX_API_KEY and SANDBOX_IMAGE are required when SANDBOX_URL is configured")
			}
		}
		if value := os.Getenv("SANDBOX_IDLE_SECONDS"); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 30 || parsed > 86400 {
				return Config{}, fmt.Errorf("SANDBOX_IDLE_SECONDS must be between 30 and 86400")
			}
			sandboxIdleSeconds = parsed
		}
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
		SandboxProvider:     sandboxProvider,
		SandboxDomain:       sandboxDomain,
		SandboxURL:          sandboxURL,
		SandboxAPIKey:       sandboxAPIKey,
		SandboxImage:        sandboxImage,
		SandboxIdleSeconds:  sandboxIdleSeconds,
	}, nil
}
