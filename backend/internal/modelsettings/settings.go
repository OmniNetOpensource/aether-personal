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

type Settings struct {
	CompactAfterHours      int32      `json:"compact_after_hours"`
	CompactAtTokens        int32      `json:"compact_at_tokens"`
	MainContextLength      int64      `json:"main_context_length"`
	SubagentContextLength  int64      `json:"subagent_context_length"`
	MainOutputLimit        int32      `json:"main_output_limit"`
	SubagentOutputLimit    int32      `json:"subagent_output_limit"`
	MainModel              string     `json:"main_model"`
	SubagentModel          *string    `json:"subagent_model"`
	EffectiveSubagentModel string     `json:"effective_subagent_model"`
	Version                int64      `json:"version"`
	UpdatedAt              *time.Time `json:"updated_at"`
	UpdatedBy              *string    `json:"updated_by"`
	Source                 string     `json:"source"`
}

func Load(ctx context.Context, queries *database.Queries, defaults config.Config) (Settings, error) {
	limit := defaults.MaxOutputTokens
	if limit == 0 {
		limit = config.DefaultMaxOutputTokens
	}
	result := Settings{CompactAfterHours: 6, CompactAtTokens: 200000, MainModel: defaults.MainModel, Source: "environment", MainOutputLimit: limit, SubagentOutputLimit: limit}
	if !defaults.SubagentFollowsMain {
		result.SubagentModel = &defaults.SubagentModel
	}
	row, err := queries.GetModelSettings(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, err
	}
	if err == nil {
		result.CompactAfterHours, result.CompactAtTokens = row.CompactAfterHours, row.CompactAtTokens
		if row.MainContextLength != nil {
			result.MainContextLength = *row.MainContextLength
		}
		if row.SubagentContextLength != nil {
			result.SubagentContextLength = *row.SubagentContextLength
		}
		result.MainModel, result.SubagentModel, result.Version = row.MainModel, row.SubagentModel, row.Version
		if row.MainModelMaxTokens != nil {
			result.MainOutputLimit = min(limit, *row.MainModelMaxTokens)
		}
		if row.SubagentModelMaxTokens != nil {
			result.SubagentOutputLimit = min(limit, *row.SubagentModelMaxTokens)
		}
		result.UpdatedAt, result.UpdatedBy, result.Source = &row.UpdatedAt, &row.UpdatedBy, "database"
	}
	result.EffectiveSubagentModel = result.MainModel
	if result.SubagentModel == nil {
		result.SubagentOutputLimit = result.MainOutputLimit
		result.SubagentContextLength = result.MainContextLength
	}
	if result.SubagentModel != nil {
		result.EffectiveSubagentModel = *result.SubagentModel
	}
	return result, nil
}
