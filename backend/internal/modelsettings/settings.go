// 本文件读取账号模型选择和部署全局的主动压缩提醒线与热压缩触发线，并在首次设置前提供各自的默认值。

package modelsettings

import (
	"context"
	"errors"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DefaultModel 由 OpenRouter 按请求能力从免费模型中路由。
const DefaultModel = "openrouter/free"

// providerModelPrefix 标识绑定到看板自定义模型供应商的模型引用，格式为 provider:<uuid>:<upstream_model>。
const providerModelPrefix = "provider:"

// chatGPTModelPrefix 标识绑定到看板 ChatGPT 订阅渠道的模型引用，格式为 chatgpt:<upstream_model>。
const chatGPTModelPrefix = "chatgpt:"

// EncodeChatGPTModel 将 ChatGPT 订阅渠道的上游模型名编码为稳定的选择标识。
func EncodeChatGPTModel(upstreamModel string) string {
	return chatGPTModelPrefix + upstreamModel
}

// ParseChatGPTModel 解析 ChatGPT 订阅渠道的模型引用；未带 chatgpt: 前缀或缺少模型名时返回 ok=false。
func ParseChatGPTModel(model string) (string, bool) {
	upstreamModel, found := strings.CutPrefix(model, chatGPTModelPrefix)
	if !found || upstreamModel == "" {
		return "", false
	}
	return upstreamModel, true
}

// EncodeProviderModel 将自定义供应商 ID 与上游真实模型名编码为稳定的选择标识，重命名供应商不影响已保存选择。
func EncodeProviderModel(providerID uuid.UUID, upstreamModel string) string {
	return providerModelPrefix + providerID.String() + ":" + upstreamModel
}

// ParseProviderModel 解析自定义供应商模型引用；未带 provider: 前缀时返回 ok=false，表示走内置 OpenRouter。
func ParseProviderModel(model string) (uuid.UUID, string, bool) {
	rest, found := strings.CutPrefix(model, providerModelPrefix)
	if !found {
		return uuid.Nil, "", false
	}
	rawID, upstreamModel, found := strings.Cut(rest, ":")
	if !found || upstreamModel == "" {
		return uuid.Nil, "", false
	}
	providerID, err := uuid.Parse(rawID)
	if err != nil {
		return uuid.Nil, "", false
	}
	return providerID, upstreamModel, true
}

// Selection 是账号选择的模型 ID 和写入版本。Subagent 留空跟随 Main。
type Selection struct {
	MainModel     string `json:"main_model"`
	SubagentModel string `json:"subagent_model"`
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
	return settings, nil
}

// RuntimeSettings 是管理看板负责的全局参数，由请求前阶段与回复后的热压缩按需读取。
type RuntimeSettings struct {
	CompactAtTokens    int32 `json:"compact_at_tokens"`
	HotCompactAtTokens int32 `json:"hot_compact_at_tokens"`
	Version            int64 `json:"version"`
}

// LoadRuntime 保留原有的部署阈值，并兼容尚未保存配置的实例。
func LoadRuntime(ctx context.Context, queries *database.Queries) (RuntimeSettings, error) {
	row, err := queries.GetRuntimeSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return RuntimeSettings{CompactAtTokens: 100000, HotCompactAtTokens: 60000}, nil
	}
	return RuntimeSettings(row), err
}
