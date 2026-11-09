// 本文件实现管理看板的模型配置接口：拉取可用模型目录，分别保存 Main 与上下文压缩的运行时模型设置，以及闲置反思时长。

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
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/jackc/pgx/v5"
)

type adminModel struct {
	CompactOnly         bool   `json:"compact_only"`
	MaxCompletionTokens *int32 `json:"max_completion_tokens"`
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Provider            string `json:"provider"`
	ContextLength       int64  `json:"context_length"`
	Pricing             struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

// adminModelCatalog 分别报告渠道故障，某一目录不可用不阻断另一个渠道的模型选择。
type adminModelCatalog struct {
	Items                []adminModel    `json:"items"`
	Warnings             []string        `json:"warnings"`
	FetchedAt            time.Time       `json:"fetched_at"`
	UnavailableProviders map[string]bool `json:"-"`
}

// availableModels 汇总已配置渠道，错误提示只包含固定文案，不暴露供应商响应或密钥。
func (s *server) availableModels(ctx context.Context) adminModelCatalog {
	catalog := adminModelCatalog{Items: []adminModel{}, Warnings: []string{}, FetchedAt: time.Now().UTC(), UnavailableProviders: map[string]bool{}}
	// 各渠道目录并行读取，共享页面请求的时限。
	type result struct {
		provider string
		models   []adminModel
		err      error
	}
	results := make(chan result, 3)
	go func() {
		credentials, err := s.services.Load(ctx, services.OpenRouter)
		var models []adminModel
		if err == nil {
			models, err = s.openRouterModels(ctx, credentials.APIKey)
		}
		results <- result{"OpenRouter", models, err}
	}()
	go func() {
		credentials, err := s.services.Load(ctx, services.IkunGemini)
		var models []adminModel
		if err == nil {
			models, err = s.ikunGeminiModels(ctx, credentials)
		}
		results <- result{"ikun Gemini", models, err}
	}()
	go func() {
		credentials, err := s.services.Load(ctx, services.IkunClaude)
		var models []adminModel
		if err == nil {
			models, err = s.ikunClaudeModels(ctx, credentials)
		}
		results <- result{"ikun Claude", models, err}
	}()
	for range 3 {
		result := <-results
		if result.err != nil {
			catalog.UnavailableProviders[result.provider] = true
			message := " 模型目录暂时不可用，请检查服务配置或稍后刷新。"
			if errors.Is(result.err, services.ErrNotConfigured) {
				message = " 尚未配置，请在服务配置页保存 Key。"
			}
			if errors.Is(result.err, services.ErrUnreadable) {
				message = " 凭证无法解密，请核对部署主密钥或替换 Key。"
			}
			catalog.Warnings = append(catalog.Warnings, result.provider+message)
		} else {
			catalog.Items = append(catalog.Items, result.models...)
		}
	}
	slices.SortFunc(catalog.Items, func(a, b adminModel) int { return strings.Compare(a.ID, b.ID) })
	return catalog
}

// openRouterModels 保留 OpenRouter 的能力筛选，既有模型 ID 不变。
func (s *server) openRouterModels(ctx context.Context, apiKey string) ([]adminModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 16<<20 {
		return nil, fmt.Errorf("model catalog is too large")
	}
	var catalog struct {
		Data []struct {
			adminModel
			SupportedParameters []string `json:"supported_parameters"`
			TopProvider         struct {
				MaxCompletionTokens *int32 `json:"max_completion_tokens"`
			} `json:"top_provider"`
			Architecture struct {
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, err
	}
	models := []adminModel{}
	for _, entry := range catalog.Data {
		if entry.ID == "" || len(entry.ID) > 160 || entry.Name == "" || utf8.RuneCountInString(entry.Name) > 200 || !slices.Contains(entry.SupportedParameters, "tools") || !slices.Contains(entry.Architecture.OutputModalities, "text") {
			continue
		}
		if entry.TopProvider.MaxCompletionTokens != nil && *entry.TopProvider.MaxCompletionTokens > 0 {
			entry.adminModel.MaxCompletionTokens = entry.TopProvider.MaxCompletionTokens
		}
		entry.adminModel.Provider = "OpenRouter"
		models = append(models, entry.adminModel)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("model catalog has no compatible models")
	}
	slices.SortFunc(models, func(a, b adminModel) int { return strings.Compare(a.ID, b.ID) })
	return models, nil
}

func (s *server) adminModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	writeAdminJSON(w, s.availableModels(ctx))
}

func (s *server) adminModelSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	settings, err := modelsettings.Load(ctx, s.queries, s.appConfig)
	if err != nil {
		internalError(w, "load model settings failed", err)
		return
	}
	writeAdminJSON(w, settings)
}

func (s *server) updateAdminModelSettings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CompactAtTokens            int32  `json:"compact_at_tokens"`
		MemoryUpdateIdleMinutes    int32  `json:"memory_update_idle_minutes"`
		RecentFollowupIdleMinutes  int32  `json:"recent_followup_idle_minutes"`
		UnknownUnknownsIdleMinutes int32  `json:"unknown_unknowns_idle_minutes"`
		MainModel                  string `json:"main_model"`
		CompactModel               string `json:"compact_model"`
		Version                    int64  `json:"version"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.MainModel == "" || len(request.MainModel) > 160 || len(request.CompactModel) > 160 || request.Version < 0 || request.CompactAtTokens < 1000 || request.CompactAtTokens > 2000000 {
		http.Error(w, "invalid model settings", http.StatusBadRequest)
		return
	}
	for _, minutes := range []int32{request.MemoryUpdateIdleMinutes, request.RecentFollowupIdleMinutes, request.UnknownUnknownsIdleMinutes} {
		if minutes < 10 || minutes > 10080 {
			http.Error(w, "invalid model settings", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 14*time.Second)
	defer cancel()
	catalog := s.availableModels(ctx)
	models := catalog.Items
	// 两种用途各自校验：Claude 当前只做压缩，空压缩模型明确表示跟随 Main。
	for _, selected := range []struct {
		id      string
		compact bool
	}{{request.MainModel, false}, {request.CompactModel, true}} {
		if selected.compact && selected.id == "" {
			continue
		}
		if slices.ContainsFunc(models, func(model adminModel) bool {
			return model.ID == selected.id && (selected.compact || !model.CompactOnly)
		}) {
			continue
		}
		provider := "OpenRouter"
		if strings.HasPrefix(selected.id, "ikun:gemini-") {
			provider = "ikun Gemini"
		}
		if strings.HasPrefix(selected.id, "ikun:claude-") {
			provider = "ikun Claude"
		}
		if catalog.UnavailableProviders[provider] {
			http.Error(w, "model catalog unavailable or model missing; settings unchanged", http.StatusBadGateway)
			return
		}
		http.Error(w, "model is unavailable or incompatible with its purpose", http.StatusBadRequest)
		return
	}
	var mainCap *int32
	var mainContext *int64
	var compactModel *string
	var compactCap *int32
	var compactContext *int64
	for _, model := range models {
		if model.ID == request.MainModel {
			mainCap = model.MaxCompletionTokens
			if model.ContextLength > 0 {
				mainContext = &model.ContextLength
			}
		}
		if model.ID == request.CompactModel {
			compactModel, compactCap = &request.CompactModel, model.MaxCompletionTokens
			if model.ContextLength > 0 {
				compactContext = &model.ContextLength
			}
		}
	}
	conflict := errors.New("model settings changed")
	var saved modelsettings.Settings
	err := pgx.BeginFunc(ctx, s.databasePool, func(tx pgx.Tx) error {
		queries := s.queries.WithTx(tx)
		if err := queries.LockModelSettings(ctx); err != nil {
			return err
		}
		current, err := modelsettings.Load(ctx, queries, s.appConfig)
		if err != nil {
			return err
		}
		if current.Version != request.Version {
			return conflict
		}
		if err := queries.SaveModelSettings(ctx, database.SaveModelSettingsParams{MainModel: request.MainModel, MainModelMaxTokens: mainCap, CompactModel: compactModel, CompactModelMaxTokens: compactCap, CompactContextLength: compactContext, CompactAtTokens: request.CompactAtTokens, MainContextLength: mainContext, MemoryUpdateIdleMinutes: request.MemoryUpdateIdleMinutes, RecentFollowupIdleMinutes: request.RecentFollowupIdleMinutes, UnknownUnknownsIdleMinutes: request.UnknownUnknownsIdleMinutes, UpdatedBy: "管理员"}); err != nil {
			return err
		}
		saved, err = modelsettings.Load(ctx, queries, s.appConfig)
		return err
	})
	if errors.Is(err, conflict) {
		http.Error(w, "settings changed; reload before applying", http.StatusConflict)
		return
	}
	if err != nil {
		internalError(w, "save model settings failed", err)
		return
	}
	slog.Info("admin changed model settings", "admin_session_id", r.Context().Value(adminSessionContextKey{}), "version", saved.Version, "main_model", saved.MainModel, "compact_model", saved.CompactModel)
	writeAdminJSON(w, saved)
}
