// 本文件提供需求登记、列表读取、模型选择与任务状态工具；登记不切换当前任务，全部变更在同一事务中提交。

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// listTasksTool 返回当前用户的任务列表，供 Main 判断新输入属于哪个已有目标。
var listTasksTool = Tool{
	Name:       "list_tasks",
	Definition: json.RawMessage(`{"type":"function","function":{"name":"list_tasks","description":"Read this user's tasks. By default returns the first 100 open tasks, in-progress first, then queued, then waiting. List order is only for display, not execution priority. When no task is in progress, read this list and choose the next queued task yourself based on the user's requests and the conversation. Set include_closed to also list completed and cancelled tasks. Tasks are current records; old tool results and summaries may be stale.","parameters":{"type":"object","additionalProperties":false,"properties":{"include_closed":{"type":"boolean","default":false}}}}}`),
	Execute:    executeListTasks,
}

// recordTaskInputTool 原子登记一个用户请求包含的全部目标和补充，全部变更一次提交。
var recordTaskInputTool = Tool{
	Name:       "record_task_input",
	Definition: json.RawMessage(`{"type":"function","function":{"name":"record_task_input","description":"Register a new user request before doing its work. Include ALL goals in this request: create a queued task for each new goal, or revise the existing task for additions/corrections to that same goal. Supply the COMPLETE updated title and description, preserving earlier constraints. A new goal never cancels or switches the in-progress task. Existing task status is preserved unless the user asks to cancel, pause (waiting, explaining why to the user in the same reply), or resume (queued). Do not revive closed tasks unless explicitly asked; a new follow-up goal can be a new task. Empty changes is only for a request containing no task change. One request may update several tasks and create several new ones. Use tool calls only during registration; do not write assistant text or answer a newly queued goal.","parameters":{"type":"object","additionalProperties":false,"properties":{"changes":{"type":"array","maxItems":20,"items":{"type":"object","additionalProperties":false,"properties":{"task_id":{"type":"string","description":"Omit for a new task; otherwise the exact existing task ID."},"title":{"type":"string","maxLength":200},"description":{"type":"string","maxLength":32000},"status":{"type":"string","enum":["queued","waiting","cancelled"],"description":"Only for an existing task and only when the user changes its lifecycle; omit to preserve its status."}},"required":["title","description"]}}},"required":["changes"]}}}`),
	Execute:    executeRecordTaskInput,
}

// updateTaskTool 让模型选定待执行任务，或变更当前任务状态；后端只约束单任务执行，不决定选择顺序。
var updateTaskTool = Tool{
	Name:       "update_task",
	Definition: json.RawMessage(`{"type":"function","function":{"name":"update_task","description":"Set status=in_progress on one queued task YOU choose, only when no task is in progress and every new user request has been registered. Read list_tasks first. Starting a task must be the only tool call in its batch, with no assistant text; begin work and user-facing text on the next turn. Other status changes apply ONLY to the in-progress task. Set completed when the requested work is finished, in a separate tool-only batch before delivering the result; this changes task status only and does not bind or register any output. Waiting requires a concrete blocker, with any necessary clarification explained in the same reply's assistant text. After a task ends, read list_tasks and choose the next queued task yourself; creation order and task ID do not define priority. Never switch away from an in-progress task just because a new request arrives. Do not work on another goal in this reply, or mark it complete. Cancellation requested by the user is handled when registering their input.","parameters":{"type":"object","additionalProperties":false,"properties":{"task_id":{"type":"string"},"status":{"type":"string","enum":["in_progress","completed","waiting"]}},"required":["task_id","status"]}}}`),
	Execute:    executeUpdateTask,
}

// TaskRecord 是任务工具返回的任务快照，只包含任务自身的要求与状态。
type TaskRecord struct {
	ID          uuid.UUID `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
}

// TaskSnapshot 把数据库任务行转成工具返回的任务快照。
func TaskSnapshot(row database.Task) TaskRecord {
	return TaskRecord{ID: row.ID, Title: row.Title, Description: row.Description, Status: row.Status}
}

// executeListTasks 按用户隔离返回任务数组，不提供按任务 ID 单独读取的入口。
func executeListTasks(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
	var input struct {
		IncludeClosed bool `json:"include_closed"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "Error: arguments must be JSON with an optional include_closed boolean", nil
	}
	rows, err := queries.ListTasks(ctx, database.ListTasksParams{UserID: userID, IncludeClosed: input.IncludeClosed})
	if err != nil {
		return "", err
	}
	records := make([]TaskRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, TaskSnapshot(row.Task))
	}
	result, err := json.Marshal(records)
	return string(result), err
}

// taskInputChange 是单个目标的完整更新；不提供状态时保留既有状态，新任务总是排队。
type taskInputChange struct {
	TaskID      *uuid.UUID `json:"task_id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
}

// executeRecordTaskInput 先检查全部变更，再写任务，业务参数错误不会留下半条登记。
func executeRecordTaskInput(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
	var input struct {
		Changes []taskInputChange `json:"changes"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil || input.Changes == nil || len(input.Changes) > 20 {
		return "Error: changes (0 to 20 items) is required", nil
	}
	// 参数和归属在写入前一次检查，随后内部代码信任这份已经归一的变更。
	seen := map[uuid.UUID]bool{}
	for index := range input.Changes {
		change := &input.Changes[index]
		change.Title = strings.TrimSpace(change.Title)
		change.Description = strings.TrimSpace(change.Description)
		if change.Title == "" || utf8.RuneCountInString(change.Title) > 200 || change.Description == "" || utf8.RuneCountInString(change.Description) > 32000 {
			return "Error: title (1 to 200 characters) and description (1 to 32000) are required", nil
		}
		if change.TaskID == nil {
			if change.Status != "" {
				return "Error: a new task is always queued; omit status", nil
			}
			continue
		}
		if seen[*change.TaskID] {
			return "Error: combine changes to the same task into one entry", nil
		}
		seen[*change.TaskID] = true
		current, err := queries.GetTask(ctx, database.GetTaskParams{UserID: userID, ID: *change.TaskID})
		if errors.Is(err, pgx.ErrNoRows) {
			return "Error: task not found for this user", nil
		}
		if err != nil {
			return "", err
		}
		if change.Status == "" {
			change.Status = current.Task.Status
		} else if change.Status != "queued" && change.Status != "waiting" && change.Status != "cancelled" {
			return "Error: registration can only queue, pause or cancel an existing task", nil
		}
		if current.Task.Status == "in_progress" && change.Status == "queued" {
			return "Error: keep the active task in progress; pause or cancel it only when the user asks", nil
		}
	}
	// 全部变更都已校验；任务变更在调用方事务内一次提交。
	records := make([]TaskRecord, 0, len(input.Changes))
	for _, change := range input.Changes {
		var taskID uuid.UUID
		var err error
		if change.TaskID == nil {
			taskID, err = queries.CreateTask(ctx, database.CreateTaskParams{UserID: userID, Title: change.Title, Description: change.Description})
		} else {
			taskID = *change.TaskID
			err = queries.ReviseTaskFromInput(ctx, database.ReviseTaskFromInputParams{UserID: userID, ID: taskID, Title: change.Title, Description: change.Description, Status: change.Status})
		}
		if err != nil {
			return "", err
		}
		row, err := queries.GetTask(ctx, database.GetTaskParams{UserID: userID, ID: taskID})
		if err != nil {
			return "", err
		}
		records = append(records, TaskSnapshot(row.Task))
	}
	result, err := json.Marshal(records)
	return string(result), err
}

// executeUpdateTask 根据模型给出的 ID 启动待办或更新当前任务；SQL 阻止任务抢占和提前完成未执行项。
func executeUpdateTask(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
	var input struct {
		TaskID uuid.UUID `json:"task_id"`
		Status string    `json:"status"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "Error: arguments must be JSON with a valid task UUID", nil
	}
	if input.TaskID == uuid.Nil || (input.Status != "in_progress" && input.Status != "completed" && input.Status != "waiting") {
		return "Error: task_id and status (in_progress/completed/waiting) are required", nil
	}
	affected, err := queries.UpdateTaskStatus(ctx, database.UpdateTaskStatusParams{UserID: userID, ID: input.TaskID, Status: input.Status})
	if err != nil {
		return "", err
	}
	if affected == 0 {
		return "Error: finish or pause only the active task; starting requires a queued task and no active task", nil
	}
	row, err := queries.GetTask(ctx, database.GetTaskParams{UserID: userID, ID: input.TaskID})
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(TaskSnapshot(row.Task))
	return string(result), err
}
