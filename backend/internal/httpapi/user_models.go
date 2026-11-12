// 本文件提供 App 的个人模型设置：验证聊天会话，读取 OpenRouter 免费目录并保存当前账号的模型选择。

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// userModel 是 App 可以选择的目录项。保存时只使用后端查询的输入容量。
type userModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength *int64 `json:"-"`
}

// requireChatUser 用现有 Bearer 会话确定操作对象，账号 ID 由服务端提供。
func (s *server) requireChatUser(next func(http.ResponseWriter, *http.Request, uuid.UUID)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || token == "" {
			http.Error(w, "请先登录", http.StatusUnauthorized)
			return
		}
		session, err := s.findSession(ctx, token)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "登录已过期，请重新登录", http.StatusUnauthorized)
			return
		}
		if err != nil {
			internalError(w, "read chat session for model settings", err)
			return
		}
		next(w, r.WithContext(ctx), session.UserID)
	}
}

// getUserModels 返回已保存的选择与当前目录，目录查询失败时仍可以切回默认路由。
func (s *server) getUserModels(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	settings, err := modelsettings.LoadUser(r.Context(), s.queries, userID)
	if err != nil {
		internalError(w, "load user model settings", err)
		return
	}
	models, err := s.freeModels(r.Context())
	var catalogNotice string
	if errors.Is(err, services.ErrNotConfigured) {
		catalogNotice = "部署尚未配置 OpenRouter API Key。"
	} else if err != nil {
		slog.Error("load free model catalog", "error", err)
		catalogNotice = "免费模型目录暂时不可用。"
	}
	writeAdminJSON(w, struct {
		Selection     modelsettings.Selection `json:"selection"`
		Models        []userModel             `json:"models"`
		CatalogNotice string                  `json:"catalog_notice"`
	}{
		Selection:     settings.Selection,
		Models:        append([]userModel{{ID: modelsettings.DefaultModel, Name: "OpenRouter 免费自动选择"}}, models...),
		CatalogNotice: catalogNotice,
	})
}

// saveUserModels 校验免费模型目录与客户端版本，一次更新该账号的三种选择。
func (s *server) saveUserModels(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	var selection modelsettings.Selection
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&selection) != nil || decoder.Decode(&struct{}{}) != io.EOF || selection.MainModel == "" || selection.Version < 0 {
		http.Error(w, "模型设置格式不正确", http.StatusBadRequest)
		return
	}

	// 默认路由和跟随选项不依赖目录可用性；具体免费型号须出现在当前工具模型目录。
	selectedModels := []string{selection.MainModel, selection.SubagentModel, selection.CompactModel}
	needsCatalog := false
	for _, id := range selectedModels {
		if id == "" || id == modelsettings.DefaultModel {
			continue
		}
		if len(id) > 160 || !strings.HasSuffix(id, ":free") {
			http.Error(w, "请选择 OpenRouter 免费模型", http.StatusBadRequest)
			return
		}
		needsCatalog = true
	}

	// 固定型号的目录容量由服务端保存；自动路由的实际型号随请求变化。
	capacities := map[string]*int64{"": nil, modelsettings.DefaultModel: nil}
	if needsCatalog {
		models, err := s.freeModels(r.Context())
		if errors.Is(err, services.ErrNotConfigured) {
			http.Error(w, "部署尚未配置 OpenRouter API Key", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			slog.Error("validate free model catalog", "error", err)
			http.Error(w, "免费模型目录暂时不可用，请稍后重试", http.StatusBadGateway)
			return
		}
		for _, model := range models {
			capacities[model.ID] = model.ContextLength
		}
	}
	for _, id := range selectedModels {
		if _, listed := capacities[id]; !listed {
			http.Error(w, "所选免费模型已不可用，请重新选择", http.StatusBadRequest)
			return
		}
	}

	version, err := s.queries.SaveUserModelSettings(r.Context(), database.SaveUserModelSettingsParams{
		UserID:                userID,
		MainModel:             selection.MainModel,
		MainContextLength:     capacities[selection.MainModel],
		SubagentModel:         selection.SubagentModel,
		SubagentContextLength: capacities[selection.SubagentModel],
		CompactModel:          selection.CompactModel,
		ExpectedVersion:       selection.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "模型设置已在其他设备更新，请重新加载", http.StatusConflict)
		return
	}
	if err != nil {
		internalError(w, "save user model settings", err)
		return
	}
	selection.Version = version
	writeAdminJSON(w, selection)
}

// freeModels 从 OpenRouter 目录保留支持工具调用的免费文本模型，供展示和保存共用。
func (s *server) freeModels(ctx context.Context) ([]userModel, error) {
	credentials, err := s.services.Load(ctx, services.OpenRouter)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog returned HTTP %d", response.StatusCode)
	}
	const maxCatalogSize = 16 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCatalogSize {
		return nil, errors.New("model catalog is too large")
	}
	var catalog struct {
		Data []struct {
			ID                  string   `json:"id"`
			Name                string   `json:"name"`
			ContextLength       int64    `json:"context_length"`
			SupportedParameters []string `json:"supported_parameters"`
			Architecture        struct {
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}
	var models []userModel
	for _, entry := range catalog.Data {
		if !strings.HasSuffix(entry.ID, ":free") || len(entry.ID) > 160 ||
			!slices.Contains(entry.SupportedParameters, "tools") || !slices.Contains(entry.Architecture.OutputModalities, "text") {
			continue
		}
		model := userModel{ID: entry.ID, Name: entry.Name}
		if entry.ContextLength > 0 {
			model.ContextLength = &entry.ContextLength
		}
		models = append(models, model)
	}
	slices.SortFunc(models, func(a, b userModel) int { return strings.Compare(a.Name, b.Name) })
	return models, nil
}
