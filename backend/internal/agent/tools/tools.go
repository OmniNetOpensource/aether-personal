// 本文件登记全部 Agent 工具：汇总每个工具文件里登记的定义与执行函数，供 Runner 按名字查找、按登记顺序把定义发给模型。

package tools

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dependencies 是 Runner 在执行工具时注入的外部依赖：连接池供事务外工具自行查库，服务凭证与联网客户端供联网工具调用外部服务。
type Dependencies struct {
	DatabasePool *pgxpool.Pool
	Services     *services.Store
	HTTPClient   *http.Client
}

// Tool 是一条工具登记：模型看到的定义、执行是在事务外还是与回执同一事务，以及调用哪个实现。
type Tool struct {
	Name       string
	Definition json.RawMessage
	// OutsideTransaction 为 true 时在事务外执行，回执单独提交。默认与回执在同一事务里提交。
	OutsideTransaction bool
	Execute            func(ctx context.Context, deps Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error)
}

// all 是工具的唯一登记表，顺序即发给模型的顺序。新增工具时在这里登记，并同步 admin.sql 里的已知工具名。
var all = []Tool{scheduleTool, searchMessageTool, readMessageTool, searchTool, searchTwitterTool, fetchURLTool}

// Definitions 返回全部工具的定义，顺序与登记表一致。
func Definitions() []json.RawMessage {
	definitions := make([]json.RawMessage, 0, len(all))
	for _, tool := range all {
		definitions = append(definitions, tool.Definition)
	}
	return definitions
}

// ByName 按名字返回登记表中的工具。
func ByName(name string) (Tool, bool) {
	for _, tool := range all {
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
