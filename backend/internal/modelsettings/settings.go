// 本文件读取账号模型选择和部署全局的压缩阈值、闲置反思时长，并在首次设置前提供各自的默认值。

package modelsettings

import (
	"context"
	"errors"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DefaultModel 由 OpenRouter 按请求能力从免费模型中路由。
const DefaultModel = "openrouter/free"

// Selection 是账号选择的模型 ID 和写入版本。Subagent 留空跟随 Main，压缩留空跟随当前 Agent。
type Selection struct {
	MainModel     string `json:"main_model"`
	SubagentModel string `json:"subagent_model"`
	CompactModel  string `json:"compact_model"`
	Version       int64  `json:"version"`
}

// UserSettings 还携带目录中的输入容量，供请求前阶段确定压缩阈值。
type UserSettings struct {
	Selection
	MainContextLength     int64
	SubagentContextLength int64
}

// LoadUser 只读取指定账号；尚无个人记录时使用免费路由及跟随规则。
func LoadUser(ctx context.Context, queries *database.Queries, userID uuid.UUID) (UserSettings, error) {
	row, err := queries.GetUserModelSettings(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserSettings{Selection: Selection{MainModel: DefaultModel}}, nil
	}
	if err != nil {
		return UserSettings{}, err
	}
	var settings UserSettings
	settings.MainModel, settings.Version = row.MainModel, row.Version
	if row.MainContextLength != nil {
		settings.MainContextLength = *row.MainContextLength
	}
	if row.SubagentModel != nil {
		settings.SubagentModel = *row.SubagentModel
	}
	if row.SubagentContextLength != nil {
		settings.SubagentContextLength = *row.SubagentContextLength
	}
	if row.CompactModel != nil {
		settings.CompactModel = *row.CompactModel
	}
	return settings, nil
}

// RuntimeSettings 是管理看板负责的全局参数，由请求前阶段与调度器各取所需。
type RuntimeSettings struct {
	CompactAtTokens       int32 `json:"compact_at_tokens"`
	ReflectionIdleMinutes int32 `json:"reflection_idle_minutes"`
	Version               int64 `json:"version"`
}

// LoadRuntime 保留原有的部署阈值，并兼容尚未保存配置的实例。
func LoadRuntime(ctx context.Context, queries *database.Queries) (RuntimeSettings, error) {
	row, err := queries.GetRuntimeSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeSettings{CompactAtTokens: 200000, ReflectionIdleMinutes: 120}, nil
	}
	return RuntimeSettings(row), err
}
