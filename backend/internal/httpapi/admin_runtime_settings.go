// 本文件提供管理看板的全局运行参数接口，管理员可以调整压缩阈值和闲置反思时长。

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/jackc/pgx/v5"
)

// adminRuntimeSettings 读取持久化的全局阈值，尚无配置时返回默认值。
func (s *server) adminRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	settings, err := modelsettings.LoadRuntime(ctx, s.queries)
	if err != nil {
		internalError(w, "load runtime settings", err)
		return
	}
	writeAdminJSON(w, settings)
}

// updateAdminRuntimeSettings 原子保存两个阈值，拒绝过期页面覆盖刚保存的版本。
func (s *server) updateAdminRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	var settings modelsettings.RuntimeSettings
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&settings) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		settings.Version < 0 || settings.CompactAtTokens < 1000 || settings.CompactAtTokens > 2000000 ||
		settings.ReflectionIdleMinutes < 10 || settings.ReflectionIdleMinutes > 10080 {
		http.Error(w, "invalid runtime settings", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	version, err := s.queries.SaveRuntimeSettings(ctx, database.SaveRuntimeSettingsParams{
		CompactAtTokens:       settings.CompactAtTokens,
		ReflectionIdleMinutes: settings.ReflectionIdleMinutes,
		ExpectedVersion:       settings.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "settings changed; reload before applying", http.StatusConflict)
		return
	}
	if err != nil {
		internalError(w, "save runtime settings", err)
		return
	}
	settings.Version = version
	slog.Info("admin changed runtime settings", "admin_session_id", r.Context().Value(adminSessionContextKey{}), "version", version)
	writeAdminJSON(w, settings)
}
