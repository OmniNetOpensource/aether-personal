// 本文件读取基础设施与模型默认值等启动配置；供应商凭证由看板保存到数据库，加密主密钥仍由部署环境提供。

package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
)

type Config struct {
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
	AgentCapacity        int32
	MainModel            string
	FrontendDir          string
	ImageCacheDir        string
	ImageCacheMaxBytes   int64
	ImageCacheTTL        time.Duration
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

	// 单进程并行用户数采用保守的连接预算；Runner 仅在短查询和事务期间使用连接。
	agentCapacity := databaseMaxConns - 4
	if value := os.Getenv("AGENT_CAPACITY"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil || parsed < 1 || parsed > int64(databaseMaxConns-4) {
			return Config{}, fmt.Errorf("AGENT_CAPACITY must be between 1 and DATABASE_MAX_CONNS - 4, got %q", value)
		}
		agentCapacity = int32(parsed)
	}

	// 磁盘缓存必须容得下一张合法图片；七天是从写入开始的绝对有效期，不随读取延长。
	imageCacheDir := os.Getenv("IMAGE_CACHE_DIR")
	if imageCacheDir == "" {
		imageCacheDir = "bin/image-cache"
	}
	imageCacheMaxBytes := int64(1 << 30)
	if value := os.Getenv("IMAGE_CACHE_MAX_BYTES"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < images.MaxImageBytes {
			return Config{}, fmt.Errorf("IMAGE_CACHE_MAX_BYTES must be at least %d, got %q", images.MaxImageBytes, value)
		}
		imageCacheMaxBytes = parsed
	}
	imageCacheTTL := 7 * 24 * time.Hour
	if value := os.Getenv("IMAGE_CACHE_TTL"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("IMAGE_CACHE_TTL must be a positive duration, got %q", value)
		}
		imageCacheTTL = parsed
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
		AdminPasswordSalt:    adminPasswordSalt,
		AdminPasswordKey:     adminPasswordKey,
		ServiceEncryptionKey: serviceEncryptionKey,
		TurnstileSiteKey:     strings.TrimSpace(os.Getenv("TURNSTILE_SITE_KEY")),
		TurnstileSecretKey:   strings.TrimSpace(os.Getenv("TURNSTILE_SECRET_KEY")),
		TurnstileHostname:    strings.ToLower(strings.TrimSpace(os.Getenv("TURNSTILE_HOSTNAME"))),
		Port:                 port,
		DatabaseURL:          databaseURL,
		DatabaseMaxConns:     databaseMaxConns,
		AgentCapacity:        agentCapacity,
		MainModel:            mainModel,
		FrontendDir:          os.Getenv("FRONTEND_DIR"),
		ImageCacheDir:        imageCacheDir,
		ImageCacheMaxBytes:   imageCacheMaxBytes,
		ImageCacheTTL:        imageCacheTTL,
	}, nil
}
