// 本文件管理看板配置的自定义模型供应商：负责增删改查、AES-GCM 凭证加解密，并将任意用户选择的模型解析为统一的 llm.Config。

package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/llm"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ModelProviderInfo 是管理看板展示的自定义模型供应商脱敏信息，不含完整 Key 或密文。
type ModelProviderInfo struct {
	ID        uuid.UUID    `json:"id"`
	Name      string       `json:"name"`
	Protocol  llm.Protocol `json:"protocol"`
	BaseURL   string       `json:"base_url"`
	Status    string       `json:"status"`
	KeySuffix string       `json:"key_suffix"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// ModelProviderEntry 是拉取模型目录时使用的自定义供应商运行配置；单条无法解密时通过 Err 单独报告。
type ModelProviderEntry struct {
	ID     uuid.UUID
	Name   string
	Config llm.Config
	Err    error
}

// modelProviderAAD 以供应商 UUID 构造附加认证数据，防止不同供应商或服务间的密文互换。
func modelProviderAAD(id uuid.UUID) []byte {
	return []byte("model_provider:" + id.String())
}

// validateModelProviderFields 校验供应商名称、协议与接口地址，并返回规范化后的值。
func validateModelProviderFields(name string, protocol llm.Protocol, baseURL, appEnv string) (string, string, error) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" || utf8.RuneCountInString(trimmedName) > 64 {
		return "", "", fmt.Errorf("%w: provider name must be between 1 and 64 characters", ErrInvalid)
	}
	if !llm.ValidProtocol(protocol) {
		return "", "", fmt.Errorf("%w: unsupported protocol", ErrInvalid)
	}
	normalizedURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if err := validateBaseURL(normalizedURL, appEnv); err != nil {
		return "", "", err
	}
	return trimmedName, normalizedURL, nil
}

// OpenRouterConfig 构造内置 OpenRouter 渠道的 llm.Config，与自定义供应商共用同一套模型协议包。
func (s *Store) OpenRouterConfig() (llm.Config, error) {
	if s.openRouterAPIKey == "" {
		return llm.Config{}, fmt.Errorf("%s: %w", OpenRouter, ErrNotConfigured)
	}
	return llm.Config{
		Protocol:        llm.ProtocolOpenAI,
		BaseURL:         OpenRouterBaseURL,
		APIKey:          s.openRouterAPIKey,
		SendSessionID:   true,
		EnableReasoning: true,
	}, nil
}

// ResolveModelConfig 将用户保存的模型标识解析为协议配置与上游真实模型名。
func (s *Store) ResolveModelConfig(ctx context.Context, selectedModel string) (llm.Config, string, error) {
	if providerID, upstreamModel, ok := modelsettings.ParseProviderModel(selectedModel); ok {
		_, config, err := s.LoadModelProviderConfig(ctx, providerID)
		if err != nil {
			return llm.Config{}, "", err
		}
		return config, upstreamModel, nil
	}
	config, err := s.OpenRouterConfig()
	if err != nil {
		return llm.Config{}, "", err
	}
	return config, selectedModel, nil
}

// LoadModelProviderConfig 读取并解密单个自定义供应商的运行时配置，供模型请求与单渠道目录校验使用。
func (s *Store) LoadModelProviderConfig(ctx context.Context, id uuid.UUID) (string, llm.Config, error) {
	row, err := s.queries.GetModelProvider(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", llm.Config{}, fmt.Errorf("model provider %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return "", llm.Config{}, err
	}
	key, err := s.cipher.Open(nil, nil, row.ApiKeyCiphertext, modelProviderAAD(row.ID))
	if err != nil {
		return row.Name, llm.Config{}, fmt.Errorf("model provider %s: %w", row.Name, ErrUnreadable)
	}
	return row.Name, llm.Config{
		Protocol: llm.Protocol(row.Protocol),
		BaseURL:  row.BaseUrl,
		APIKey:   string(key),
	}, nil
}

// ListModelProviderConfigs 列出所有自定义供应商的运行时配置，单条密钥无法解密时只标记该项错误，不阻断其他供应商。
func (s *Store) ListModelProviderConfigs(ctx context.Context) ([]ModelProviderEntry, error) {
	rows, err := s.queries.ListModelProviders(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]ModelProviderEntry, 0, len(rows))
	for _, row := range rows {
		entry := ModelProviderEntry{ID: row.ID, Name: row.Name}
		key, err := s.cipher.Open(nil, nil, row.ApiKeyCiphertext, modelProviderAAD(row.ID))
		if err != nil {
			entry.Err = fmt.Errorf("model provider %s: %w", row.Name, ErrUnreadable)
		} else {
			entry.Config = llm.Config{
				Protocol: llm.Protocol(row.Protocol),
				BaseURL:  row.BaseUrl,
				APIKey:   string(key),
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// ListModelProviders 生成看板展示的脱敏供应商列表，并检测每条密文当前能否用部署主密钥解开。
func (s *Store) ListModelProviders(ctx context.Context) ([]ModelProviderInfo, error) {
	rows, err := s.queries.ListModelProviders(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]ModelProviderInfo, 0, len(rows))
	for _, row := range rows {
		status := "saved"
		if _, err := s.cipher.Open(nil, nil, row.ApiKeyCiphertext, modelProviderAAD(row.ID)); err != nil {
			status = "unreadable"
		}
		items = append(items, ModelProviderInfo{
			ID:        row.ID,
			Name:      row.Name,
			Protocol:  llm.Protocol(row.Protocol),
			BaseURL:   row.BaseUrl,
			Status:    status,
			KeySuffix: row.KeySuffix,
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		})
	}
	return items, nil
}

// CreateModelProvider 新增一条自定义模型供应商并加密保存其 API Key。
func (s *Store) CreateModelProvider(ctx context.Context, name string, protocol llm.Protocol, baseURL, apiKey string) (ModelProviderInfo, error) {
	trimmedName, normalizedURL, err := validateModelProviderFields(name, protocol, baseURL, s.appEnv)
	if err != nil {
		return ModelProviderInfo{}, err
	}
	if err := validateAPIKey(apiKey); err != nil {
		return ModelProviderInfo{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return ModelProviderInfo{}, err
	}
	ciphertext := s.cipher.Seal(nil, nil, []byte(apiKey), modelProviderAAD(id))
	row, err := s.queries.CreateModelProvider(ctx, database.CreateModelProviderParams{
		ID:               id,
		Name:             trimmedName,
		Protocol:         string(protocol),
		BaseUrl:          normalizedURL,
		ApiKeyCiphertext: ciphertext,
		KeySuffix:        keySuffix(apiKey),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ModelProviderInfo{}, fmt.Errorf("%w: provider name already exists", ErrConflict)
		}
		return ModelProviderInfo{}, err
	}
	return ModelProviderInfo{
		ID:        row.ID,
		Name:      row.Name,
		Protocol:  llm.Protocol(row.Protocol),
		BaseURL:   row.BaseUrl,
		Status:    "saved",
		KeySuffix: row.KeySuffix,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

// UpdateModelProvider 更新自定义模型供应商配置；apiKey 为 nil 或空字符串时保留原加密凭证。
func (s *Store) UpdateModelProvider(ctx context.Context, id uuid.UUID, name string, protocol llm.Protocol, baseURL string, apiKey *string) (ModelProviderInfo, error) {
	existing, err := s.queries.GetModelProvider(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelProviderInfo{}, ErrNotFound
	}
	if err != nil {
		return ModelProviderInfo{}, err
	}
	trimmedName, normalizedURL, err := validateModelProviderFields(name, protocol, baseURL, s.appEnv)
	if err != nil {
		return ModelProviderInfo{}, err
	}
	ciphertext, suffix := existing.ApiKeyCiphertext, existing.KeySuffix
	if apiKey != nil && *apiKey != "" {
		if err := validateAPIKey(*apiKey); err != nil {
			return ModelProviderInfo{}, err
		}
		ciphertext = s.cipher.Seal(nil, nil, []byte(*apiKey), modelProviderAAD(id))
		suffix = keySuffix(*apiKey)
	}
	row, err := s.queries.UpdateModelProvider(ctx, database.UpdateModelProviderParams{
		ID:               id,
		Name:             trimmedName,
		Protocol:         string(protocol),
		BaseUrl:          normalizedURL,
		ApiKeyCiphertext: ciphertext,
		KeySuffix:        suffix,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ModelProviderInfo{}, fmt.Errorf("%w: provider name already exists", ErrConflict)
		}
		return ModelProviderInfo{}, err
	}
	status := "saved"
	if _, err := s.cipher.Open(nil, nil, row.ApiKeyCiphertext, modelProviderAAD(row.ID)); err != nil {
		status = "unreadable"
	}
	return ModelProviderInfo{
		ID:        row.ID,
		Name:      row.Name,
		Protocol:  llm.Protocol(row.Protocol),
		BaseURL:   row.BaseUrl,
		Status:    status,
		KeySuffix: row.KeySuffix,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

// DeleteModelProvider 删除指定 ID 的自定义模型供应商。
func (s *Store) DeleteModelProvider(ctx context.Context, id uuid.UUID) error {
	deleted, err := s.queries.DeleteModelProvider(ctx, id)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrNotFound
	}
	return nil
}
