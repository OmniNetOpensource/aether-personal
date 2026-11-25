// 本文件将共享记忆引擎登记为 Main 完整读写工具与 Subagent 只读工具，复用同一套逻辑与事务锁。

package tools

import (
	"context"
	"encoding/json"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/memory"
	"github.com/google/uuid"
)

// memoryTool 向 Main 暴露六个记忆文件命令，沿用 Runner 的事务并叠加用户记忆锁，与外部 MCP 并发写入互斥。
var memoryTool = Tool{
	Name:       memory.ToolName,
	Definition: buildMemoryToolDefinition(),
	Execute: func(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
		output, _, err := memory.ExecuteInTx(ctx, queries, userID, arguments)
		return output, err
	},
}

// subagentMemoryTool 向 Subagent 仅暴露只读 view 命令，避免多个并行 Subagent 同时改写同一记忆文件造成覆盖冲突。
var subagentMemoryTool = Tool{
	Name:               memory.ToolName,
	Definition:         json.RawMessage(`{"type":"function","function":{"name":"memory","description":"Read persistent text files in this user's private /memories namespace (read-only view access for subagents). /memories/memory.md is the root containing core agreements and topic links; view it first only when user preferences or prior context would help your assigned task, then view only relevant linked topics.","parameters":{"type":"object","additionalProperties":false,"properties":{"command":{"type":"string","enum":["view"]},"path":{"type":"string"},"view_range":{"type":"array","items":{"type":"integer"},"minItems":2,"maxItems":2}},"required":["command","path"]}}}`),
	OutsideTransaction: true,
	Execute: func(ctx context.Context, deps Dependencies, _ *database.Queries, userID uuid.UUID, arguments string) (string, error) {
		var check struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(arguments), &check); err != nil {
			return "Error: memory arguments must be valid JSON", nil
		}
		if check.Command != "view" {
			return "Error: subagents only have read-only view access to memory", nil
		}
		output, _, err := memory.Execute(ctx, deps.DatabasePool, userID, arguments)
		return output, err
	},
}

// buildMemoryToolDefinition 从共享 memory 包的元数据生成 OpenAI 函数工具定义，确保与 MCP 暴露的 Schema 始终一致。
func buildMemoryToolDefinition() json.RawMessage {
	encoded, err := json.Marshal(struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}{
		Type: "function",
		Function: struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}{
			Name:        memory.ToolName,
			Description: memory.ToolDescription,
			Parameters:  memory.InputSchema,
		},
	})
	if err != nil {
		panic(err)
	}
	return encoded
}
