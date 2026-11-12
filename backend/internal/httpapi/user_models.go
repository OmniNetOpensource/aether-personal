// 本文件提供 App 的个人模型设置：验证聊天会话，读取已配置的模型目录并保存当前账号的模型选择。

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

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/providers"
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
	// 已配置渠道独立返回目录，一家失败时仍可以选择其他渠道。
	models, failures := s.modelCatalog(r.Context(), nil)
	var notices []string
	for _, failure := range failures {
		if errors.Is(failure.err, services.ErrNotConfigured) {
			if failure.service == services.OpenRouter {
				notices = append(notices, "部署尚未配置 OpenRouter API Key。")
			}
			continue
		}
		slog.Error("load model catalog", "service", failure.service, "error", failure.err)
		notices = append(notices, failure.name+" 模型目录暂时不可用。")
	}
	slices.Sort(notices)

	writeAdminJSON(w, struct {
		Selection     modelsettings.Selection `json:"selection"`
		Models        []userModel             `json:"models"`
		CatalogNotice string                  `json:"catalog_notice"`
	}{
		Selection:     settings.Selection,
		Models:        append([]userModel{{ID: modelsettings.DefaultModel, Name: "OpenRouter 免费自动选择"}}, models...),
		CatalogNotice: strings.Join(notices, " "),
	})
}

// saveUserModels 校验所选渠道的模型目录与客户端版本，一次更新该账号的三种选择。
func (s *server) saveUserModels(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	var selection modelsettings.Selection
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&selection) != nil || decoder.Decode(&struct{}{}) != io.EOF || selection.MainModel == "" || selection.Version < 0 {
		http.Error(w, "模型设置格式不正确", http.StatusBadRequest)
		return
	}

	// 默认路由和跟随选项始终可保存。具体型号按实际请求使用的渠道校验。
	selectedModels := []string{selection.MainModel, selection.SubagentModel, selection.CompactModel}
	selectedProviders := map[string]bool{}
	for _, id := range selectedModels {
		if id == "" || id == modelsettings.DefaultModel {
			continue
		}
		if len(id) > 160 {
			http.Error(w, "模型 ID 过长", http.StatusBadRequest)
			return
		}
		selectedProviders[providers.ForModel(id).Service()] = true
	}
	capacities := map[string]*int64{"": nil, modelsettings.DefaultModel: nil}
	if len(selectedProviders) > 0 {
		models, failures := s.modelCatalog(r.Context(), selectedProviders)
		for _, failure := range failures {
			if errors.Is(failure.err, services.ErrNotConfigured) {
				http.Error(w, failure.name+" 尚未配置", http.StatusServiceUnavailable)
				return
			}
			slog.Error("validate model catalog", "service", failure.service, "error", failure.err)
			http.Error(w, failure.name+" 模型目录暂时不可用，请稍后重试", http.StatusBadGateway)
			return
		}
		for _, model := range models {
			capacities[model.ID] = model.ContextLength
		}
	}
	for _, id := range selectedModels {
		if _, listed := capacities[id]; !listed {
			http.Error(w, "所选模型已不可用，请重新选择", http.StatusBadRequest)
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

// catalogFailure 记录目录不可用的渠道，App 仅显示固定文案。
type catalogFailure struct {
	service string
	name    string
	err     error
}

// modelCatalog 并行读取所需渠道。requested 为空时读取全部已配置渠道。
func (s *server) modelCatalog(ctx context.Context, requested map[string]bool) ([]userModel, []catalogFailure) {
	sources := []struct {
		service string
		name    string
		fetch   func(context.Context, services.Settings) ([]userModel, error)
	}{
		{services.OpenRouter, "OpenRouter", s.openRouterModels},
		{services.IkunGemini, "ikun Gemini", s.ikunGeminiModels},
		{services.IkunClaude, "ikun Claude", s.ikunClaudeModels},
	}
	type result struct {
		models  []userModel
		failure catalogFailure
	}
	results := make(chan result, len(sources))
	count := 0
	for _, source := range sources {
		if requested != nil && !requested[source.service] {
			continue
		}
		count++
		go func() {
			credentials, err := s.services.Load(ctx, source.service)
			var models []userModel
			if err == nil {
				models, err = source.fetch(ctx, credentials)
			}
			results <- result{models, catalogFailure{source.service, source.name, err}}
		}()
	}
	var models []userModel
	var failures []catalogFailure
	for range count {
		result := <-results
		if result.failure.err != nil {
			failures = append(failures, result.failure)
			continue
		}
		models = append(models, result.models...)
	}
	slices.SortFunc(models, func(a, b userModel) int { return strings.Compare(a.Name, b.Name) })
	return models, failures
}

// openRouterModels 保留原看板的工具能力条件，价格不参与目录筛选。
func (s *server) openRouterModels(ctx context.Context, credentials services.Settings) ([]userModel, error) {
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
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}
	var models []userModel
	for _, entry := range catalog.Data {
		if entry.ID == "" || entry.ID == modelsettings.DefaultModel || len(entry.ID) > 160 ||
			!slices.Contains(entry.SupportedParameters, "tools") {
			continue
		}
		model := userModel{ID: entry.ID, Name: entry.Name}
		if entry.ContextLength > 0 {
			model.ContextLength = &entry.ContextLength
		}
		models = append(models, model)
	}
	return models, nil
}
