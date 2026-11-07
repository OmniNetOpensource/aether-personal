// 本文件负责运行时模型设置：读取管理员保存在数据库中的模型配置，覆盖环境变量默认值，供每次模型请求使用。

package modelsettings

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// Settings 分别记录主对话与压缩的模型能力：OpenRouter 省略输出额度，ikun 模型使用已知最大值；空压缩模型表示跟随 Main。
type Settings struct {
	CompactAtTokens      int32      `json:"compact_at_tokens"`
	MainContextLength    int64      `json:"main_context_length"`
	EffectiveOutputLimit int32      `json:"effective_output_limit"`
	CompactOutputLimit   int32      `json:"compact_output_limit"`
	CompactContextLength int64      `json:"compact_context_length"`
	CompactModel         string     `json:"compact_model"`
	MainModel            string     `json:"main_model"`
	Version              int64      `json:"version"`
	UpdatedAt            *time.Time `json:"updated_at"`
	UpdatedBy            *string    `json:"updated_by"`
	Source               string     `json:"source"`
}

// Load 读取看板保存的模型与目录能力；没有记录时使用默认模型，不另设输出额度。
func Load(ctx context.Context, queries *database.Queries, defaults config.Config) (Settings, error) {
	result := Settings{CompactAtTokens: 200000, MainModel: defaults.MainModel, Source: "environment"}
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
		if row.CompactModel != nil {
			result.CompactModel = *row.CompactModel
		}
		if row.CompactContextLength != nil {
			result.CompactContextLength = *row.CompactContextLength
		}
		// Claude 的 max_tokens 为必填项，使用目录最大值；OpenRouter 仍省略额度参数。
		if strings.HasPrefix(result.CompactModel, "ikun:") && row.CompactModelMaxTokens != nil {
			result.CompactOutputLimit = *row.CompactModelMaxTokens
		}
		// OpenRouter 省略 max_tokens，避免目录最大输出量与当前输入相加超过总上下文容量。
		if strings.HasPrefix(row.MainModel, "ikun:gemini-") && row.MainModelMaxTokens != nil {
			result.EffectiveOutputLimit = *row.MainModelMaxTokens
		}
		result.UpdatedAt, result.UpdatedBy, result.Source = &row.UpdatedAt, &row.UpdatedBy, "database"
	}
	return result, nil
}
