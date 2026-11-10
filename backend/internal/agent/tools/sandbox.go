// 本文件登记固定版本 AIO 提供的工具；读取定义不会启动沙箱，实际调用才恢复用户的长期电脑。

package tools

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
)

// sandboxCatalog 来自 aiod 0.9.2+51c1e169d797 的 tools/list，数组元素类型补齐为供应商要求的 JSON Schema。
//
//go:embed sandbox_tools.json
var sandboxCatalog []byte

// sandboxTools 将 MCP 原始定义转换为应用已有的 function 结构，所有电脑操作共享同一条有序队列。
func sandboxTools() []Tool {
	var catalog []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(sandboxCatalog, &catalog); err != nil {
		panic(err)
	}
	registered := make([]Tool, 0, len(catalog))
	for _, entry := range catalog {
		definition, err := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": entry.Name, "description": entry.Description, "parameters": entry.InputSchema}})
		if err != nil {
			panic(err)
		}
		registered = append(registered, Tool{Name: entry.Name, Definition: definition, OutsideTransaction: true, Ordered: true, ContentArray: true,
			Execute: func(ctx context.Context, deps Dependencies, _ *database.Queries, userID uuid.UUID, arguments string) (string, error) {
				if !json.Valid([]byte(arguments)) {
					return "Error: arguments must be JSON", nil
				}
				return deps.Sandbox.Call(ctx, userID, entry.Name, arguments)
			}})
	}
	return registered
}
