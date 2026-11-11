// 本文件登记固定版本 AIO 提供的工具以及工作区产物发送工具；读取定义不会启动沙箱，实际调用才恢复用户的长期电脑。

package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
)

// sandboxCatalog 来自 aiod 0.9.2+51c1e169d797 的 tools/list，数组元素类型补齐为供应商要求的 JSON Schema。
//
//go:embed sandbox_tools.json
var sandboxCatalog []byte

// sandboxSendFileTool 把 /mnt/workspace 中的最终文件作为独立聊天气泡发送给用户，与电脑命令共用有序队列。
var sandboxSendFileTool = Tool{
	Name:               "sandbox_send_file",
	Definition:         json.RawMessage(`{"type":"function","function":{"name":"sandbox_send_file","description":"Send a finished file from /mnt/workspace to the user in chat as a standalone downloadable file card (or an inline image preview when the file is a PNG, JPEG, GIF or WebP up to 4 MiB). Maximum file size is 50 MiB; archive directories first.","parameters":{"type":"object","additionalProperties":false,"properties":{"path":{"type":"string","description":"Absolute path of an existing non-empty file inside /mnt/workspace, for example /mnt/workspace/report.pdf"}},"required":["path"]}}}`),
	OutsideTransaction: true,
	Ordered:            true,
	UserVisibleParts:   true,
	Execute: func(ctx context.Context, deps Dependencies, _ *database.Queries, userID uuid.UUID, arguments string) (string, error) {
		var parsed struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
			return "Error: arguments must be JSON", nil
		}
		if strings.TrimSpace(parsed.Path) == "" {
			return "Error: path is required", nil
		}
		summary, parts, err := deps.Sandbox.SendFile(ctx, userID, parsed.Path)
		if err != nil || len(parts) == 0 {
			return summary, err
		}
		encoded, err := json.Marshal(map[string]any{"content": summary, "parts": parts})
		return string(encoded), err
	},
}

// sandboxTools 将 MCP 原始定义转换为应用已有的 function 结构，所有电脑操作与文件发送共享同一条有序队列。
func sandboxTools() []Tool {
	var catalog []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(sandboxCatalog, &catalog); err != nil {
		panic(err)
	}
	registered := make([]Tool, 0, len(catalog)+1)
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
	registered = append(registered, sandboxSendFileTool)
	return registered
}
