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
	"github.com/OmniNetOpensource/aether-personal/backend/internal/sandbox"
	"github.com/redis/go-redis/v9"
)

type Config struct {
	Sandbox              sandbox.Config
	AdminPasswordSalt    []byte
	AdminPasswordKey     []byte
	ServiceEncryptionKey []byte
	TurnstileSiteKey     string
	TurnstileSecretKey   string
	TurnstileHostname    string
	AppEnv               string
	Port                 string
	DatabaseURL          string
	RedisURL             string
	DatabaseMaxConns     int32
	AgentCapacity        int32
	MainModel            string
	FrontendDir          string
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

	// 服务进程必须配置 REDIS_URL；导入命令不涉及跨实例信号，留空时跳过解析。
	redisURL := strings.TrimSpace(os.Getenv("REDIS_URL"))
	if redisURL != "" {
		if _, err := redis.ParseURL(redisURL); err != nil {
			return Config{}, fmt.Errorf("invalid REDIS_URL: %w", err)
		}
	}

	databaseMaxConns := int32(20)
	if value := os.Getenv("DATABASE_MAX_CONNS"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil || parsed < 5 {
			return Config{}, fmt.Errorf("DATABASE_MAX_CONNS must be at least 5, got %q", value)
		}
		databaseMaxConns = int32(parsed)
	}

	// 单实例并行用户数采用保守的连接预算；Runner 仅在短查询和事务期间使用连接。
	agentCapacity := databaseMaxConns - 4
	if value := os.Getenv("AGENT_CAPACITY"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil || parsed < 1 || parsed > int64(databaseMaxConns-4) {
			return Config{}, fmt.Errorf("AGENT_CAPACITY must be between 1 and DATABASE_MAX_CONNS - 4, got %q", value)
		}
		agentCapacity = int32(parsed)
	}

	// Redis 图片缓存容量须容得下一张合法图片，默认 128 MiB；TTL 为从写入起算的绝对有效期，读取不续期。
	imageCacheMaxBytes := int64(128 << 20)
	if value := os.Getenv("IMAGE_CACHE_MAX_BYTES"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < images.MaxImageBytes {
			return Config{}, fmt.Errorf("IMAGE_CACHE_MAX_BYTES must be at least %d, got %q", images.MaxImageBytes, value)
		}
		imageCacheMaxBytes = parsed
	}
	imageCacheTTL := 24 * time.Hour
	if value := os.Getenv("IMAGE_CACHE_TTL"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Second {
			return Config{}, fmt.Errorf("IMAGE_CACHE_TTL must be at least 1s, got %q", value)
		}
		imageCacheTTL = parsed
	}

	// 沙箱可暂不配置；一旦调用工具，运行时检查地域、模板和 VPC 是否完整。
	sandboxTimeout := 10 * time.Minute
	if value := os.Getenv("SANDBOX_TIMEOUT"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Minute || parsed > time.Hour {
			return Config{}, fmt.Errorf("SANDBOX_TIMEOUT must be between 1m and 1h")
		}
		sandboxTimeout = parsed
	}
	var switches []string
	for _, value := range strings.Split(os.Getenv("SANDBOX_VSWITCH_IDS"), ",") {
		if value = strings.TrimSpace(value); value != "" {
			switches = append(switches, value)
		}
	}
	sandboxConfig := sandbox.Config{Region: strings.TrimSpace(os.Getenv("SANDBOX_REGION")), Template: strings.TrimSpace(os.Getenv("SANDBOX_TEMPLATE")),
		VPCID: strings.TrimSpace(os.Getenv("SANDBOX_VPC_ID")), SecurityGroupID: strings.TrimSpace(os.Getenv("SANDBOX_SECURITY_GROUP_ID")), VSwitchIDs: switches, Timeout: sandboxTimeout}

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
		Sandbox:              sandboxConfig,
		AppEnv:               appEnv,
		AdminPasswordSalt:    adminPasswordSalt,
		AdminPasswordKey:     adminPasswordKey,
		ServiceEncryptionKey: serviceEncryptionKey,
		TurnstileSiteKey:     strings.TrimSpace(os.Getenv("TURNSTILE_SITE_KEY")),
		TurnstileSecretKey:   strings.TrimSpace(os.Getenv("TURNSTILE_SECRET_KEY")),
		TurnstileHostname:    strings.ToLower(strings.TrimSpace(os.Getenv("TURNSTILE_HOSTNAME"))),
		Port:                 port,
		DatabaseURL:          databaseURL,
		RedisURL:             redisURL,
		DatabaseMaxConns:     databaseMaxConns,
		AgentCapacity:        agentCapacity,
		MainModel:            mainModel,
		FrontendDir:          os.Getenv("FRONTEND_DIR"),
		ImageCacheMaxBytes:   imageCacheMaxBytes,
		ImageCacheTTL:        imageCacheTTL,
	}, nil
}
