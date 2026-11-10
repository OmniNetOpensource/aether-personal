// 本文件负责运行时模型设置：读取管理员保存在数据库中的模型配置与三种闲置后台工作的时长，覆盖默认值，供每次模型请求和调度器使用。

package modelsettings

import (
	"context"
	"errors"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/config"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// Settings 分别记录主对话与压缩的模型及输入容量；输出额度统一由供应商决定，空压缩模型表示跟随 Main。
// 三个 IdleMinutes 是用户最近一条消息之后多久，调度器依次投记忆整理、近期跟进与未知的未知触发。
type Settings struct {
	CompactAtTokens            int32      `json:"compact_at_tokens"`
	MemoryUpdateIdleMinutes    int32      `json:"memory_update_idle_minutes"`
	RecentFollowupIdleMinutes  int32      `json:"recent_followup_idle_minutes"`
	UnknownUnknownsIdleMinutes int32      `json:"unknown_unknowns_idle_minutes"`
	MainContextLength          int64      `json:"main_context_length"`
	CompactContextLength       int64      `json:"compact_context_length"`
	CompactModel               string     `json:"compact_model"`
	MainModel                  string     `json:"main_model"`
	Version                    int64      `json:"version"`
	UpdatedAt                  *time.Time `json:"updated_at"`
	UpdatedBy                  *string    `json:"updated_by"`
	Source                     string     `json:"source"`
}

// Load 读取看板保存的模型与目录能力；没有记录时使用默认模型，不另设输出额度。
func Load(ctx context.Context, queries *database.Queries, defaults config.Config) (Settings, error) {
	result := Settings{CompactAtTokens: 200000, MemoryUpdateIdleMinutes: 60, RecentFollowupIdleMinutes: 120, UnknownUnknownsIdleMinutes: 180, MainModel: defaults.MainModel, Source: "environment"}
	row, err := queries.GetModelSettings(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, err
	}
	if err == nil {
		result.CompactAtTokens = row.CompactAtTokens
		result.MemoryUpdateIdleMinutes, result.RecentFollowupIdleMinutes, result.UnknownUnknownsIdleMinutes = row.MemoryUpdateIdleMinutes, row.RecentFollowupIdleMinutes, row.UnknownUnknownsIdleMinutes
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
		result.UpdatedAt, result.UpdatedBy, result.Source = &row.UpdatedAt, &row.UpdatedBy, "database"
	}
	return result, nil
}
