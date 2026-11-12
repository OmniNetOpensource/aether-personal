// 本文件登记 Main 与 Subagent 各自可用的工具集合：按角色提供对应的定义与查找入口，保证 Subagent 严格限制在只读调研范围内。

package tools

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/images"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/sandbox"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dependencies 是 Runner 的外部依赖：连接池供工具查库，凭证与客户端供联网请求，共享图片存储负责 OSS 与 Redis 缓存。
type Dependencies struct {
	DatabasePool *pgxpool.Pool
	Services     *services.Store
	HTTPClient   *http.Client
	Images       *images.Store
	Sandbox      *sandbox.Store
}

// Tool 是一条工具登记：模型看到的定义、执行是在事务外还是与回执同一事务，以及调用哪个实现。
type Tool struct {
	Name       string
	Definition json.RawMessage
	// OutsideTransaction 为 true 时在事务外执行，回执单独提交。默认与回执在同一事务里提交。
	OutsideTransaction bool
	// Ordered 在同一批次中按模型给出的顺序执行，适用于共享电脑状态的操作。
	Ordered bool
	// ContentArray 表示成功结果使用文字和图片组成的内容数组。
	ContentArray bool
	// UserVisibleParts 表示工具可返回对用户可见的聊天内容气泡（编码为含 content 与 parts 的 JSON）。
	UserVisibleParts bool
	Execute          func(ctx context.Context, deps Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error)
}

// all 是 Main 的完整工具登记表，顺序即发给 Main 模型的顺序。
var all = append([]Tool{
	listTasksTool,
	recordTaskInputTool,
	updateTaskTool,
	spawnSubagentTool,
	forwardSubagentTool,
	cancelSubagentTool,
	listSubagentsTool,
	searchMessageTool,
	readMessageTool,
	memoryTool,
	scheduleTool,
	searchTool,
	searchTwitterTool,
	fetchURLTool,
	readImageTool,
}, sandboxTools()...)

// subagentAll 是 Subagent 的只读调研工具登记表：禁止电脑操作、写记忆、改任务/日程及继续派生子代理。
var subagentAll = []Tool{
	searchMessageTool,
	readMessageTool,
	subagentMemoryTool,
	searchTool,
	searchTwitterTool,
	fetchURLTool,
	readImageTool,
}

// Definitions 返回 Main 全部工具的定义，顺序与登记表一致。
func Definitions() []json.RawMessage {
	definitions := make([]json.RawMessage, 0, len(all))
	for _, tool := range all {
		definitions = append(definitions, tool.Definition)
	}
	return definitions
}

// SubagentDefinitions 返回 Subagent 只读调研工具的定义，顺序与 subagentAll 一致。
func SubagentDefinitions() []json.RawMessage {
	definitions := make([]json.RawMessage, 0, len(subagentAll))
	for _, tool := range subagentAll {
		definitions = append(definitions, tool.Definition)
	}
	return definitions
}

// ByName 按名字查找 Main 可用的工具。
func ByName(name string) (Tool, bool) {
	for _, tool := range all {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

// SubagentByName 按名字查找 Subagent 可用的只读工具，拦截越权调用。
func SubagentByName(name string) (Tool, bool) {
	for _, tool := range subagentAll {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

// RecordedName 把模型点出的工具名收成调用日志认识的名字，未登记的归为 other。
func RecordedName(name string) string {
	if _, ok := ByName(name); ok {
		return name
	}
	return "other"
}
