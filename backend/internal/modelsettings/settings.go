// 本文件负责运行时模型设置：读取管理员保存在数据库中的模型配置，覆盖环境变量默认值，供每次模型请求使用。

package modelsettings

import (
	"context"
	"errors"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// Settings 是运行时实际生效的模型设置：Main 模型、输出上限、上下文容量与压缩阈值。
type Settings struct {
	CompactAtTokens   int32      `json:"compact_at_tokens"`
	MainContextLength int64      `json:"main_context_length"`
	MainOutputLimit   int32      `json:"main_output_limit"`
	MainModel         string     `json:"main_model"`
	Version           int64      `json:"version"`
	UpdatedAt         *time.Time `json:"updated_at"`
	UpdatedBy         *string    `json:"updated_by"`
	Source            string     `json:"source"`
}

// Load 读取看板保存的设置；数据库里还没有记录时用环境变量默认值，输出上限始终受部署上限约束。
func Load(ctx context.Context, queries *database.Queries, defaults config.Config) (Settings, error) {
	limit := defaults.MaxOutputTokens
	if limit == 0 {
		limit = config.DefaultMaxOutputTokens
	}
	result := Settings{CompactAtTokens: 200000, MainModel: defaults.MainModel, Source: "environment", MainOutputLimit: limit}
	row, err := queries.GetModelSettings(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, err
	}
	if err == nil {
		result.CompactAtTokens = row.CompactAtTokens
		if row.MainContextLength != nil {
			result.MainContextLength = *row.MainContextLength
		}
		result.MainModel, result.Version = row.MainModel, row.Version
		if row.MainModelMaxTokens != nil {
			result.MainOutputLimit = min(limit, *row.MainModelMaxTokens)
		}
		result.UpdatedAt, result.UpdatedBy, result.Source = &row.UpdatedAt, &row.UpdatedBy, "database"
	}
	return result, nil
}
