// 本命令把旧环境变量中的服务凭证一次性加密导入数据库；已有或已清除的配置均不覆盖，不输出凭证明文。

package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

// main 只做显式迁移，日常启动和请求不执行导入，因此旧环境变量不会恢复被清除的 Key。
func main() {
	settings, err := config.Load()
	if err != nil {
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, settings.DatabaseURL)
	if err != nil {
		slog.Error("connect database failed")
		os.Exit(1)
	}
	defer pool.Close()
	store, err := services.New(pool, settings.ServiceEncryptionKey, settings.AppEnv)
	if err != nil {
		slog.Error("initialize service encryption failed")
		os.Exit(1)
	}
	failed := false
	for _, definition := range services.Definitions {
		value := services.Settings{APIKey: os.Getenv(definition.LegacyKey)}
		if definition.ID == services.IkunGemini {
			value.BaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("GEMINI_BASE_URL_IKUNCODE")), "/")
		}
		if definition.ID == services.Resend {
			value.EmailFrom = strings.TrimSpace(os.Getenv("EMAIL_FROM"))
		}
		imported, err := store.Import(ctx, definition.ID, value)
		if err != nil {
			slog.Error("service import failed; check its configuration", "service", definition.ID)
			failed = true
			continue
		}
		slog.Info("service import finished", "service", definition.ID, "imported", imported)
	}
	if failed {
		os.Exit(1)
	}
}
