// 本文件提供 App 的个人模型设置：验证聊天会话，通过统一的 llm 包并行拉取内置 OpenRouter 与自定义模型供应商目录，并保存当前账号的模型选择。

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/llm"
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

// catalogSource 描述一次目录拉取任务：内置 OpenRouter 与每个自定义供应商都统一表达为一份 llm.Config。
type catalogSource struct {
	id         string
	name       string
	providerID *uuid.UUID
	config     llm.Config
	err        error
}

// catalogFailure 记录目录不可用的来源，App 仅显示固定文案。
type catalogFailure struct {
	id   string
	name string
	err  error
}

// getUserModels 返回已保存的选择与当前目录，单个来源失败时仍可以选择其他来源或默认路由。
func (s *server) getUserModels(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	settings, err := modelsettings.LoadUser(r.Context(), s.queries, userID)
	if err != nil {
		internalError(w, "load user model settings", err)
		return
	}
	customProviders, err := s.services.ListModelProviderConfigs(r.Context())
	if err != nil {
		internalError(w, "load model providers for catalog", err)
		return
	}
	sources := make([]catalogSource, 0, 1+len(customProviders))
	openRouterCfg, openRouterErr := s.services.OpenRouterConfig()
	sources = append(sources, catalogSource{
		id:     services.OpenRouter,
		name:   "OpenRouter",
		config: openRouterCfg,
		err:    openRouterErr,
	})
	for _, provider := range customProviders {
		id := provider.ID
		sources = append(sources, catalogSource{
			id:         id.String(),
			name:       provider.Name,
			providerID: &id,
			config:     provider.Config,
			err:        provider.Err,
		})
	}
	models, failures := s.fetchModelCatalogs(r.Context(), sources)
	var notices []string
	for _, failure := range failures {
		if errors.Is(failure.err, services.ErrNotConfigured) {
			if failure.id == services.OpenRouter {
				notices = append(notices, "部署尚未配置 OpenRouter API Key。")
			}
			continue
		}
		slog.Error("load model catalog", "source", failure.id, "error", failure.err)
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

// saveUserModels 校验所选来源的模型目录与客户端版本，一次更新该账号的模型选择。
func (s *server) saveUserModels(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	var selection modelsettings.Selection
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&selection) != nil || decoder.Decode(&struct{}{}) != io.EOF || selection.MainModel == "" || selection.Version < 0 {
		http.Error(w, "模型设置格式不正确", http.StatusBadRequest)
		return
	}

	// 默认路由和跟随选项始终可保存；具体型号按它指向的 OpenRouter 或自定义供应商实时校验。
	selectedModels := []string{selection.MainModel, selection.SubagentModel}
	needOpenRouter := false
	neededProviders := map[uuid.UUID]bool{}
	for _, id := range selectedModels {
		if id == "" || id == modelsettings.DefaultModel {
			continue
		}
		if len(id) > 256 {
			http.Error(w, "模型 ID 过长", http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(id, "provider:") {
			providerID, _, ok := modelsettings.ParseProviderModel(id)
			if !ok {
				http.Error(w, "所选模型已不可用，请重新选择", http.StatusBadRequest)
				return
			}
			neededProviders[providerID] = true
			continue
		}
		needOpenRouter = true
	}

	var sources []catalogSource
	if needOpenRouter {
		cfg, err := s.services.OpenRouterConfig()
		sources = append(sources, catalogSource{id: services.OpenRouter, name: "OpenRouter", config: cfg, err: err})
	}
	for providerID := range neededProviders {
		name, cfg, err := s.services.LoadModelProviderConfig(r.Context(), providerID)
		if errors.Is(err, services.ErrNotFound) {
			http.Error(w, "所选模型已不可用，请重新选择", http.StatusBadRequest)
			return
		}
		idCopy := providerID
		sources = append(sources, catalogSource{id: idCopy.String(), name: name, providerID: &idCopy, config: cfg, err: err})
	}

	capacities := map[string]*int64{"": nil, modelsettings.DefaultModel: nil}
	if len(sources) > 0 {
		models, failures := s.fetchModelCatalogs(r.Context(), sources)
		for _, failure := range failures {
			if errors.Is(failure.err, services.ErrNotConfigured) {
				http.Error(w, failure.name+" 尚未配置", http.StatusServiceUnavailable)
				return
			}
			slog.Error("validate model catalog", "source", failure.id, "error", failure.err)
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

// fetchModelCatalogs 并行拉取指定来源的模型目录，并为自定义供应商编码 provider:<uuid>:<model> 标识与展示后缀。
func (s *server) fetchModelCatalogs(ctx context.Context, sources []catalogSource) ([]userModel, []catalogFailure) {
	type result struct {
		models  []userModel
		failure catalogFailure
	}
	results := make(chan result, len(sources))
	for _, source := range sources {
		go func() {
			if source.err != nil {
				results <- result{failure: catalogFailure{id: source.id, name: source.name, err: source.err}}
				return
			}
			catalog, err := llm.ListModels(ctx, s.httpClient, source.config)
			if err != nil {
				results <- result{failure: catalogFailure{id: source.id, name: source.name, err: err}}
				return
			}
			items := make([]userModel, 0, len(catalog))
			for _, entry := range catalog {
				if source.providerID == nil {
					if entry.ID == modelsettings.DefaultModel {
						continue
					}
					items = append(items, userModel{ID: entry.ID, Name: entry.Name, ContextLength: entry.ContextLength})
					continue
				}
				encodedID := modelsettings.EncodeProviderModel(*source.providerID, entry.ID)
				if len(encodedID) > 256 {
					continue
				}
				items = append(items, userModel{
					ID:            encodedID,
					Name:          entry.Name + " · " + source.name,
					ContextLength: entry.ContextLength,
				})
			}
			results <- result{models: items}
		}()
	}
	var models []userModel
	var failures []catalogFailure
	for range len(sources) {
		outcome := <-results
		if outcome.failure.err != nil {
			failures = append(failures, outcome.failure)
			continue
		}
		models = append(models, outcome.models...)
	}
	slices.SortFunc(models, func(a, b userModel) int { return strings.Compare(a.Name, b.Name) })
	return models, failures
}
