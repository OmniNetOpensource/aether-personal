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
// MainOutputLimit 是管理员填写的上限（没在看板保存过时是部署默认值），EffectiveOutputLimit 是实际请求用的值：再与所选模型已知的最大输出取较小。
type Settings struct {
	CompactAtTokens      int32      `json:"compact_at_tokens"`
	MainContextLength    int64      `json:"main_context_length"`
	MainOutputLimit      int32      `json:"main_output_limit"`
	EffectiveOutputLimit int32      `json:"effective_output_limit"`
	MainModel            string     `json:"main_model"`
	Version              int64      `json:"version"`
	UpdatedAt            *time.Time `json:"updated_at"`
	UpdatedBy            *string    `json:"updated_by"`
	Source               string     `json:"source"`
}

// Load 读取看板保存的设置；数据库里还没有记录、或还没在看板保存过输出上限时，用环境变量给出的部署默认值。
func Load(ctx context.Context, queries *database.Queries, defaults config.Config) (Settings, error) {
	configuredOutputLimit := defaults.MaxOutputTokens
	if configuredOutputLimit == 0 {
		configuredOutputLimit = config.DefaultMaxOutputTokens
	}
	result := Settings{CompactAtTokens: 200000, MainModel: defaults.MainModel, Source: "environment", MainOutputLimit: configuredOutputLimit, EffectiveOutputLimit: configuredOutputLimit}
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
		if row.MainOutputLimit != nil {
			result.MainOutputLimit = *row.MainOutputLimit
		}
		result.EffectiveOutputLimit = result.MainOutputLimit
		if row.MainModelMaxTokens != nil {
			result.EffectiveOutputLimit = min(result.MainOutputLimit, *row.MainModelMaxTokens)
		}
		result.UpdatedAt, result.UpdatedBy, result.Source = &row.UpdatedAt, &row.UpdatedBy, "database"
	}
	return result, nil
}
