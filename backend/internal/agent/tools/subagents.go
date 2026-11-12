// 本文件实现 Main 编排后台 Subagent 的四个数据库工具：派发独立子任务、零改写直通转发产出、中途取消与状态查询。

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// spawnSubagentTool 在当前用户消息事务内原子创建 Subagent 记录与首条指令消息；事务提交后由 Runner 推入 Redis 唤醒队列。
var spawnSubagentTool = Tool{
	Name:       "spawn_subagent",
	Definition: json.RawMessage(`{"type":"function","function":{"name":"spawn_subagent","description":"Spawn an asynchronous background subagent with its own isolated context window for read-only web research, chat history lookup, memory viewing, or analysis. Returns immediately with subagent_id while the subagent runs in the background; when it finishes (or fails), the runtime automatically delivers a subagent_event message with its full output to wake you. You may call spawn_subagent multiple times in the same turn to run independent tasks or research angles in parallel. Subagents do NOT see your conversation automatically and do NOT have computer/sandbox, schedule, task, or memory-write tools—include every objective, constraint, URL,oss:// reference, and background detail the subagent needs inside prompt.","parameters":{"type":"object","additionalProperties":false,"properties":{"title":{"type":"string","maxLength":200,"description":"Concise title naming this subagent's assignment (1 to 200 characters)."},"prompt":{"type":"string","maxLength":32000,"description":"Complete, self-contained instructions for the subagent: exact goal, user constraints, relevant context from chat, and whether it should write a complete user-ready answer (for an independent task) or focused research findings (for multi-angle synthesis)."}},"required":["title","prompt"]}}}`),
	Execute:    executeSpawnSubagent,
}

// forwardSubagentTool 使用 UserVisibleParts 将已完成 Subagent 的完整原文直接作为阿芒聊天气泡推送给用户，不经 Main 重新流式生成，也不在 Main 上下文中保存第二份正文拷贝。
var forwardSubagentTool = Tool{
	Name:             "forward_subagent",
	Definition:       json.RawMessage(`{"type":"function","function":{"name":"forward_subagent","description":"Deliver a completed subagent's output verbatim to the user as a standalone assistant chat message without rewriting or regenerating it. Use this whenever a subagent handled an independent user task and its output is accurate, complete, and ready for the person to read—avoiding paraphrasing loss and duplicate generation latency. Do NOT simultaneously repeat the forwarded content in your own assistant text.","parameters":{"type":"object","additionalProperties":false,"properties":{"subagent_id":{"type":"string","description":"UUID of the completed subagent whose output should be sent verbatim to chat."}},"required":["subagent_id"]}}}`),
	UserVisibleParts: true,
	Execute:          executeForwardSubagent,
}

// cancelSubagentTool 让 Main 在用户取消或更改需求时立即中止仍在后台运行的 Subagent，避免继续消耗模型与搜索额度。
var cancelSubagentTool = Tool{
	Name:       "cancel_subagent",
	Definition: json.RawMessage(`{"type":"function","function":{"name":"cancel_subagent","description":"Immediately stop a running background subagent when the user cancels its task or changes direction so its ongoing work is no longer needed.","parameters":{"type":"object","additionalProperties":false,"properties":{"subagent_id":{"type":"string","description":"UUID of the subagent to cancel."}},"required":["subagent_id"]}}}`),
	Execute:    executeCancelSubagent,
}

// listSubagentsTool 列出当前用户近期的 Subagent 状态，供 Main 在上下文压缩后或排查进展时核对。
var listSubagentsTool = Tool{
	Name:       "list_subagents",
	Definition: json.RawMessage(`{"type":"function","function":{"name":"list_subagents","description":"List this user's recent background subagents with their IDs, titles, statuses (running, completed, failed, cancelled) and timestamps.","parameters":{"type":"object","additionalProperties":false,"properties":{}}}}`),
	Execute:    executeListSubagents,
}

// SubagentReceipt 是 spawn_subagent 与 cancel_subagent 返回给模型并供提交后信号提取的结构。
type SubagentReceipt struct {
	SubagentID uuid.UUID  `json:"subagent_id"`
	Title      string     `json:"title"`
	Status     string     `json:"status"`
	WasRunning bool       `json:"was_running,omitempty"`
	Error      *string    `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// executeSpawnSubagent 校验标题与任务描述后，在同一数据库事务中写入 subagents 行及其初始 user 消息。
func executeSpawnSubagent(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
	var input struct {
		Title  string `json:"title"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "Error: arguments must be JSON with title and prompt", nil
	}
	title := strings.TrimSpace(input.Title)
	prompt := strings.TrimSpace(input.Prompt)
	if title == "" || utf8.RuneCountInString(title) > 200 || prompt == "" || utf8.RuneCountInString(prompt) > 32000 {
		return "Error: title (1 to 200 characters) and prompt (1 to 32000 characters) are required", nil
	}
	created, err := queries.CreateSubagent(ctx, database.CreateSubagentParams{UserID: userID, Title: title})
	if err != nil {
		return "", err
	}
	initialContent, err := json.Marshal(map[string]string{"role": "user", "content": prompt})
	if err != nil {
		return "", err
	}
	sender := "main"
	if err := queries.InsertAgentMessage(ctx, database.InsertAgentMessageParams{
		UserID:     userID,
		SubagentID: &created.ID,
		Role:       "user",
		Content:    initialContent,
		Sender:     &sender,
		Visible:    false,
	}); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(SubagentReceipt{
		SubagentID: created.ID,
		Title:      title,
		Status:     "running",
		StartedAt:  created.StartedAt,
	})
	return string(encoded), err
}

// executeForwardSubagent 读取当前用户已完成 Subagent 的完整输出，封装为对聊天流可见的文本气泡与简短模型回执。
func executeForwardSubagent(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
	var input struct {
		SubagentID string `json:"subagent_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "Error: arguments must be JSON with subagent_id", nil
	}
	subagentID, err := uuid.Parse(strings.TrimSpace(input.SubagentID))
	if err != nil || subagentID == uuid.Nil {
		return "Error: subagent_id must be a valid UUID", nil
	}
	subagent, err := queries.GetSubagentForUser(ctx, database.GetSubagentForUserParams{UserID: userID, ID: subagentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "Error: subagent not found for this user", nil
	}
	if err != nil {
		return "", err
	}
	if subagent.Status != "completed" || subagent.Output == nil || strings.TrimSpace(*subagent.Output) == "" {
		return fmt.Sprintf("Error: subagent %s is %s and has no completed output to forward", subagent.ID, subagent.Status), nil
	}
	encoded, err := json.Marshal(map[string]any{
		"content": fmt.Sprintf("Forwarded subagent %s (%s) output verbatim to the user.", subagent.ID, subagent.Title),
		"parts":   []map[string]string{{"type": "text", "text": *subagent.Output}},
	})
	return string(encoded), err
}

// executeCancelSubagent 在用户锁之后获取 Subagent 锁，将仍在运行的 Subagent 标为 cancelled 并清理其未结束记录。
func executeCancelSubagent(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
	var input struct {
		SubagentID string `json:"subagent_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "Error: arguments must be JSON with subagent_id", nil
	}
	subagentID, err := uuid.Parse(strings.TrimSpace(input.SubagentID))
	if err != nil || subagentID == uuid.Nil {
		return "Error: subagent_id must be a valid UUID", nil
	}
	subagent, err := queries.GetSubagentForUser(ctx, database.GetSubagentForUserParams{UserID: userID, ID: subagentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "Error: subagent not found for this user", nil
	}
	if err != nil {
		return "", err
	}
	if err := queries.LockSubagent(ctx, subagentID); err != nil {
		return "", err
	}
	affected, err := queries.CancelSubagent(ctx, database.CancelSubagentParams{SubagentID: subagentID, UserID: userID})
	if err != nil {
		return "", err
	}
	if affected == 1 {
		if err := queries.InterruptModelCalls(ctx, database.InterruptModelCallsParams{UserID: userID, SubagentID: &subagentID}); err != nil {
			return "", err
		}
		if err := queries.DiscardGeneratingReply(ctx, database.DiscardGeneratingReplyParams{UserID: userID, SubagentID: &subagentID}); err != nil {
			return "", err
		}
		cancelledReason := "cancelled by Main"
		if err := queries.FailUnfinishedSteps(ctx, database.FailUnfinishedStepsParams{UserID: userID, SubagentID: &subagentID, Error: &cancelledReason}); err != nil {
			return "", err
		}
		subagent, err = queries.GetSubagentForUser(ctx, database.GetSubagentForUserParams{UserID: userID, ID: subagentID})
		if err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(SubagentReceipt{
		SubagentID: subagent.ID,
		Title:      subagent.Title,
		Status:     subagent.Status,
		WasRunning: affected == 1,
		Error:      subagent.Error,
		StartedAt:  subagent.StartedAt,
		FinishedAt: subagent.FinishedAt,
	})
	return string(encoded), err
}

// executeListSubagents 返回当前用户近期的全部 Subagent 状态摘要。
func executeListSubagents(ctx context.Context, _ Dependencies, queries *database.Queries, userID uuid.UUID, arguments string) (string, error) {
	if strings.TrimSpace(arguments) != "" && !json.Valid([]byte(arguments)) {
		return "Error: arguments must be valid JSON", nil
	}
	rows, err := queries.ListUserSubagents(ctx, userID)
	if err != nil {
		return "", err
	}
	items := make([]SubagentReceipt, 0, len(rows))
	for _, row := range rows {
		items = append(items, SubagentReceipt{
			SubagentID: row.ID,
			Title:      row.Title,
			Status:     row.Status,
			Error:      row.Error,
			StartedAt:  row.StartedAt,
			FinishedAt: row.FinishedAt,
		})
	}
	encoded, err := json.Marshal(items)
	return string(encoded), err
}
