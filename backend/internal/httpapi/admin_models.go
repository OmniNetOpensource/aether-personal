// 本文件实现管理看板的模型配置接口：拉取可用模型目录，读取并保存 Main 与 Subagent 的运行时模型设置。

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
	"github.com/jackc/pgx/v5"
)

type adminModel struct {
	MaxCompletionTokens *int32 `json:"max_completion_tokens"`
	ID                  string `json:"id"`
	Name                string `json:"name"`
	ContextLength       int64  `json:"context_length"`
	Pricing             struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

func (s *server) availableModels(ctx context.Context) ([]adminModel, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+s.appConfig.OpenRouterAPIKey)
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
	models, err := s.availableModels(ctx)
	if err != nil {
		slog.Error("load model catalog failed", "error", err)
		http.Error(w, "model catalog unavailable; retry later", http.StatusBadGateway)
		return
	}
	writeAdminJSON(w, map[string]any{"items": models, "fetched_at": time.Now().UTC()})
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
		CompactAfterHours int32   `json:"compact_after_hours"`
		CompactAtTokens   int32   `json:"compact_at_tokens"`
		MainModel         string  `json:"main_model"`
		SubagentModel     *string `json:"subagent_model"`
		Version           int64   `json:"version"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.MainModel == "" || len(request.MainModel) > 160 || request.Version < 0 || request.CompactAfterHours < 1 || request.CompactAfterHours > 8760 || request.CompactAtTokens < 1000 || request.CompactAtTokens > 2000000 || (request.SubagentModel != nil && (*request.SubagentModel == "" || len(*request.SubagentModel) > 160)) {
		http.Error(w, "invalid model settings", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 14*time.Second)
	defer cancel()
	models, err := s.availableModels(ctx)
	if err != nil {
		slog.Error("validate model catalog failed", "error", err)
		http.Error(w, "model catalog unavailable; settings unchanged", http.StatusBadGateway)
		return
	}
	for _, id := range []*string{&request.MainModel, request.SubagentModel} {
		if id != nil && !slices.ContainsFunc(models, func(model adminModel) bool { return model.ID == *id }) {
			http.Error(w, "model is unavailable or incompatible with agent capabilities", http.StatusBadRequest)
			return
		}
	}
	var mainCap, subagentCap *int32
	var mainContext, subagentContext *int64
	for _, model := range models {
		if model.ID == request.MainModel {
			mainCap = model.MaxCompletionTokens
			mainContext = &model.ContextLength
		}
		if request.SubagentModel != nil && model.ID == *request.SubagentModel {
			subagentCap = model.MaxCompletionTokens
			subagentContext = &model.ContextLength
		}
	}
	conflict := errors.New("model settings changed")
	var saved modelsettings.Settings
	err = pgx.BeginFunc(ctx, s.databasePool, func(tx pgx.Tx) error {
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
		if err := queries.SaveModelSettings(ctx, database.SaveModelSettingsParams{MainModel: request.MainModel, SubagentModel: request.SubagentModel, MainModelMaxTokens: mainCap, SubagentModelMaxTokens: subagentCap, CompactAfterHours: request.CompactAfterHours, CompactAtTokens: request.CompactAtTokens, MainContextLength: mainContext, SubagentContextLength: subagentContext, UpdatedBy: "管理员"}); err != nil {
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
	slog.Info("admin changed model settings", "admin_session_id", r.Context().Value(adminSessionContextKey{}), "version", saved.Version, "main_model", saved.MainModel, "subagent_model", saved.EffectiveSubagentModel)
	writeAdminJSON(w, saved)
}
