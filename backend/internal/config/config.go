// 本文件读取基础设施与模型默认值等启动配置；供应商凭证由看板保存到数据库，加密主密钥仍由部署环境提供。

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
	MaxOutputTokens      int32
	AdminPasswordSalt    []byte
	AdminPasswordKey     []byte
	ServiceEncryptionKey []byte
	TurnstileSiteKey     string
	TurnstileSecretKey   string
	TurnstileHostname    string
	AppEnv               string
	Port                 string
	DatabaseURL          string
	DatabaseMaxConns     int32
	MainModel            string
	SubagentModel        string
	SubagentFollowsMain  bool
	FrontendDir          string
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

	// 缺少服务 Key 不阻止管理员进入看板；只有解密所需的部署主密钥必须先存在。
	serviceEncryptionKey, err := base64.StdEncoding.DecodeString(os.Getenv("SERVICE_ENCRYPTION_KEY"))
	if err != nil || len(serviceEncryptionKey) != 32 {
		return Config{}, fmt.Errorf("SERVICE_ENCRYPTION_KEY must be a base64-encoded 32-byte key")
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
		AppEnv:               appEnv,
		MaxOutputTokens:      maxOutputTokens,
		AdminPasswordSalt:    adminPasswordSalt,
		AdminPasswordKey:     adminPasswordKey,
		ServiceEncryptionKey: serviceEncryptionKey,
		TurnstileSiteKey:     strings.TrimSpace(os.Getenv("TURNSTILE_SITE_KEY")),
		TurnstileSecretKey:   strings.TrimSpace(os.Getenv("TURNSTILE_SECRET_KEY")),
		TurnstileHostname:    strings.ToLower(strings.TrimSpace(os.Getenv("TURNSTILE_HOSTNAME"))),
		Port:                 port,
		DatabaseURL:          databaseURL,
		DatabaseMaxConns:     databaseMaxConns,
		MainModel:            mainModel,
		SubagentModel:        subagentModel,
		SubagentFollowsMain:  os.Getenv("SUBAGENT_MODEL") == "",
		FrontendDir:          os.Getenv("FRONTEND_DIR"),
	}, nil
}
