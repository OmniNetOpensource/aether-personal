// 本文件实现服务配置管理：单管理员可替换或清除部署共用的 Key，响应仅返回脱敏状态。

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
)

// adminServices 允许查看缺失及无法解密的凭证状态，完整 Key 与密文均不返回浏览器。
func (s *server) adminServices(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, err := s.services.List(ctx)
	if err != nil {
		internalError(w, "load service settings failed", err)
		return
	}
	writeAdminJSON(w, map[string]any{"items": items})
}

// updateAdminService 的空 Key 表示清除，省略或 null 表示保留；单条服务配置一起保存。
func (s *server) updateAdminService(w http.ResponseWriter, r *http.Request) {
	service := r.PathValue("service")
	if !slices.ContainsFunc(services.Definitions, func(d services.Definition) bool { return d.ID == service }) {
		http.NotFound(w, r)
		return
	}
	var request struct {
		APIKey    *string `json:"api_key"`
		BaseURL   *string `json:"base_url"`
		EmailFrom *string `json:"email_from"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || (request.APIKey == nil && request.BaseURL == nil && request.EmailFrom == nil) {
		http.Error(w, "invalid service settings", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.services.Update(ctx, service, request.APIKey, request.BaseURL, request.EmailFrom); err != nil {
		if errors.Is(err, services.ErrInvalid) {
			http.Error(w, "invalid service settings", http.StatusBadRequest)
			return
		}
		internalError(w, "save service settings failed", err)
		return
	}
	// 审计只记录服务和操作，不记录 Key、请求正文或接口地址。
	operation := "save"
	if request.APIKey != nil && *request.APIKey == "" {
		operation = "clear"
	}
	slog.Info("admin changed service settings", "admin_session_id", r.Context().Value(adminSessionContextKey{}), "service", service, "operation", operation)
	w.WriteHeader(http.StatusNoContent)
}
