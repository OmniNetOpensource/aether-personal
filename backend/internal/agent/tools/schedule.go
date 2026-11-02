// 本文件登记 schedule 工具：定义模型看到的参数形状，具体创建、查看、修改与删除逻辑在 scheduler 包里，与工具回执同一事务执行。

package tools

import (
	"context"
	"encoding/json"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/scheduler"
	"github.com/google/uuid"
)

// scheduleTool 登记 schedule：定时安排的读写必须与回执一起提交，所以不设 OutsideTransaction。
var scheduleTool = Tool{
	Name:       "schedule",
	Definition: json.RawMessage(`{"type":"function","function":{"name":"schedule","description":"Arrange future context delivery to this user's Main. Supports one-time timestamps or recurring five-field cron rules in an explicit IANA timezone. Create requires title, context, timezone and exactly one of at or cron. List returns up to 50 items and next_before for pagination. Update requires id and changed fields; enabled=false pauses, enabled=true resumes. Delete requires id. Pausing/deleting does not cancel context already delivered. A triggered status means delivered to Main, not task completed. Missed recurring triggers are combined into one delivery after downtime.","parameters":{"type":"object","additionalProperties":false,"properties":{"action":{"type":"string","enum":["create","list","update","delete"]},"id":{"type":"string","description":"Schedule UUID for update or delete, from create or list."},"before":{"type":"string","description":"For list only: previous page's next_before UUID."},"title":{"type":"string","minLength":1,"maxLength":160},"context":{"type":"string","minLength":1,"maxLength":24000,"description":"Self-contained task instructions, necessary background, source links, constraints and expected delivery."},"at":{"type":"string","description":"One-time RFC3339 timestamp with explicit offset, in the future. Mutually exclusive with cron."},"cron":{"type":"string","description":"Recurring rule: minute hour day-of-month month day-of-week, e.g. 0 9 * * 1-5. No seconds or @every. Mutually exclusive with at."},"timezone":{"type":"string","description":"Explicit IANA timezone such as Asia/Shanghai or UTC. Required for create."},"enabled":{"type":"boolean","description":"false to pause future triggers; true to resume."}},"required":["action"]}}}`),
	Execute: func(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
		return scheduler.ExecuteTool(ctx, queries, userID, arguments)
	},
}
