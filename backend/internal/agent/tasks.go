// 本文件把实时任务状态接入 Main 请求，任务由模型选择、逐项执行；记忆文件仅通过工具按需读取。

package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// pendingTaskInput 保留待登记消息的原文和稳定 ID，即使 compact 已压缩那段历史也能准确登记。
type pendingTaskInput struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"message"`
}

// buildTaskRequest 在历史尾部附上实时任务状态，保留登记、任务选择与实际执行三个阶段的工具边界。
func (r *Runner) buildTaskRequest(ctx context.Context, rows []database.ListAgentContextRowsRow, model string, outputLimit int32) (modelRequest, error) {
	tx, err := r.databasePool.Begin(ctx)
	if err != nil {
		return modelRequest{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	queries := database.New(tx)
	if err := queries.LockUserMessages(ctx, r.userID); err != nil {
		return modelRequest{}, err
	}
	state := struct {
		ActiveTask         *tools.TaskRecord `json:"active_task"`
		NextInput          *pendingTaskInput `json:"next_unregistered_input"`
		NeedsTaskSelection bool              `json:"needs_task_selection"`
	}{}
	active, err := queries.GetActiveTask(ctx, r.userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return modelRequest{}, err
	}
	if err == nil {
		record := tools.TaskSnapshot(active.Task, active.RelatedMessageIds)
		state.ActiveTask = &record
	}
	input, err := queries.GetNextTaskInput(ctx, r.userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return modelRequest{}, err
	}
	if err == nil {
		state.NextInput = &pendingTaskInput{ID: input.ID.String(), Content: input.Content}
	}
	// 没有当前任务时只告诉模型是否有待办，不替它挑选；模型通过读取任务列表和状态工具作出选择。
	if state.ActiveTask == nil && state.NextInput == nil {
		state.NeedsTaskSelection, err = queries.HasQueuedTasks(ctx, r.userID)
		if err != nil {
			return modelRequest{}, err
		}
	}
	snapshot, err := json.Marshal(state)
	if err != nil {
		return modelRequest{}, err
	}
	// 尚未登记的原始输入可能已被 compact 移出当前世代；此时补回原生消息，图片仍按正常内容块解析。
	taskContext := []json.RawMessage{}
	if state.NextInput != nil {
		inHistory := false
		for _, row := range rows {
			if row.ID == input.ID {
				inHistory = true
				break
			}
		}
		if !inHistory {
			taskContext = append(taskContext, requestMessage(database.ListAgentContextRowsRow{Content: input.Content}))
		}
	}
	taskContext = append(taskContext, runtimeMessage("task_context", taskContextPrompt+string(snapshot)))
	prepared, err := buildAgentRequest(rows, model, outputLimit, taskContext, state.NextInput != nil)
	if err != nil {
		return modelRequest{}, err
	}
	// 登记可以涉及多个需求，只有实际执行当前任务时才允许产生用户回复；任务归属随请求快照固定。
	if state.NextInput == nil && state.ActiveTask != nil {
		prepared.TaskID = &state.ActiveTask.ID
	}
	// 尚有输入或任务时不能空转；没有任务时允许静默结束，未来文件交付也不需要额外正文才能收尾。
	prepared.RequireOutput = state.NextInput != nil || state.ActiveTask != nil || state.NeedsTaskSelection
	if state.NeedsTaskSelection {
		prepared.Tools = tools.TaskSelectionDefinitions()
	}
	return prepared, tx.Commit(ctx)
}

// taskBatchRestrictions 在执行前确定本批允许的工具：先登记，任务启动与结束各自独占一批，避免选择和执行混在一起。
// 已经开始的工具批次不因后来到达的输入中断；下一次模型决策再登记这些输入。
func (r *Runner) taskBatchRestrictions(ctx context.Context, queries *database.Queries, calls []toolCall) (map[string]string, error) {
	blocked := map[string]string{}
	_, err := queries.GetNextTaskInput(ctx, r.userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	hasInput := err == nil
	_, err = queries.GetActiveTask(ctx, r.userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	hasActive := err == nil
	for _, call := range calls {
		name := call.Function.Name
		if hasInput && name != "list_tasks" && name != "record_task_input" {
			blocked[call.ID] = "Error: register pending input before selecting or executing a task."
		} else if !hasActive && !hasInput && name != "list_tasks" && name != "update_task" {
			blocked[call.ID] = "Error: no active task; read list_tasks and select one queued task with update_task(status=in_progress) before executing work."
		}
		if name == "update_task" && len(calls) > 1 {
			var change struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal([]byte(call.Function.Arguments), &change)
			if change.Status == "completed" || change.Status == "waiting" || (!hasActive && change.Status == "in_progress") {
				for _, item := range calls {
					blocked[item.ID] = "Error: start, complete or pause a task in a separate tool batch; do not combine a task transition with other work."
				}
				break
			}
		}
	}
	return blocked, nil
}
