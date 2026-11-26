// 本文件实现管理看板的自定义模型供应商管理接口：支持列出、新增、更新与删除，响应仅返回脱敏状态。

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/llm"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
)

// adminModelProviders 返回所有已配置的自定义模型供应商的脱敏状态。
func (s *server) adminModelProviders(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, err := s.services.ListModelProviders(ctx)
	if err != nil {
		internalError(w, "load model providers failed", err)
		return
	}
	writeAdminJSON(w, map[string]any{"items": items})
}

// createAdminModelProvider 新增一条自定义模型供应商配置，完整 Key 仅入密文、不回显也不记日志。
func (s *server) createAdminModelProvider(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name     string       `json:"name"`
		Protocol llm.Protocol `json:"protocol"`
		BaseURL  string       `json:"base_url"`
		APIKey   string       `json:"api_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid model provider", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	created, err := s.services.CreateModelProvider(ctx, request.Name, request.Protocol, request.BaseURL, request.APIKey)
	if err != nil {
		writeModelProviderError(w, "create model provider failed", err)
		return
	}
	slog.Info("admin created model provider", "admin_session_id", r.Context().Value(adminSessionContextKey{}), "provider_id", created.ID, "protocol", created.Protocol)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

// updateAdminModelProvider 更新自定义模型供应商的名称、协议、地址或可选的新 API Key。
func (s *server) updateAdminModelProvider(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid model provider ID", http.StatusBadRequest)
		return
	}
	var request struct {
		Name     string       `json:"name"`
		Protocol llm.Protocol `json:"protocol"`
		BaseURL  string       `json:"base_url"`
		APIKey   *string      `json:"api_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid model provider", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	updated, err := s.services.UpdateModelProvider(ctx, id, request.Name, request.Protocol, request.BaseURL, request.APIKey)
	if err != nil {
		writeModelProviderError(w, "update model provider failed", err)
		return
	}
	slog.Info("admin updated model provider", "admin_session_id", r.Context().Value(adminSessionContextKey{}), "provider_id", updated.ID, "protocol", updated.Protocol)
	writeAdminJSON(w, updated)
}

// deleteAdminModelProvider 删除一条自定义模型供应商配置。
func (s *server) deleteAdminModelProvider(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid model provider ID", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.services.DeleteModelProvider(ctx, id); err != nil {
		writeModelProviderError(w, "delete model provider failed", err)
		return
	}
	slog.Info("admin deleted model provider", "admin_session_id", r.Context().Value(adminSessionContextKey{}), "provider_id", id)
	w.WriteHeader(http.StatusNoContent)
}

// writeModelProviderError 将模型供应商校验、不存在与重名错误映射为对应 HTTP 状态码。
func writeModelProviderError(w http.ResponseWriter, logMessage string, err error) {
	switch {
	case errors.Is(err, services.ErrInvalid):
		http.Error(w, "invalid model provider", http.StatusBadRequest)
	case errors.Is(err, services.ErrNotFound):
		http.Error(w, "model provider not found", http.StatusNotFound)
	case errors.Is(err, services.ErrConflict):
		http.Error(w, "model provider name already exists", http.StatusConflict)
	default:
		internalError(w, logMessage, err)
	}
}
