// 本文件是 agent loop 的请求前阶段：按 Main 或 Subagent 隔离收拾上次中断残留、补跑缺回执的工具，
// 再读对应角色模型设置、必要时压缩上下文，并组装这一次的模型请求。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// beforeRequest 是请求前阶段的入口：把当前 Agent 的数据库状态整理到可以发请求，开一条步骤，再产出这一次的请求。
// through 随本轮实际读到的消息更新，失败时由请求后阶段据此停住；准备失败时已开的步骤交给失败收尾结束。
func (r *Runner) beforeRequest(ctx, budget context.Context, through *int64) (modelRequest, uuid.UUID, error) {
	queries := database.New(r.databasePool)
	// 在校验本轮租约代数的短事务内收拾上次中断留下的记录，防止已失去租约的旧实例误清接管实例正在运行的步骤；
	// 必须排在开新步骤之前，否则新步骤也会被标成中断。
	err := pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		txQueries := database.New(tx)
		if err := r.lockAndVerifyLease(ctx, txQueries); err != nil {
			return err
		}
		if err := txQueries.DiscardGeneratingReply(ctx, database.DiscardGeneratingReplyParams{UserID: r.userID, SubagentID: r.subagentID}); err != nil {
			return fmt.Errorf("recover incomplete reply: %w", err)
		}
		interrupted := "interrupted before the step finished"
		if err := txQueries.FailUnfinishedSteps(ctx, database.FailUnfinishedStepsParams{UserID: r.userID, SubagentID: r.subagentID, Error: &interrupted}); err != nil {
			return fmt.Errorf("recover unfinished steps: %w", err)
		}
		return nil
	})
	if err != nil {
		return modelRequest{}, uuid.Nil, err
	}
	// 进程中断时可能停在一批工具中间：先补跑没有回执的工具，请求里才不会出现没有回执的调用。
	rows, err := queries.ListAgentContextRows(ctx, database.ListAgentContextRowsParams{UserID: r.userID, SubagentID: r.subagentID})
	if err != nil {
		return modelRequest{}, uuid.Nil, err
	}
	for _, row := range rows {
		*through = max(*through, row.Sequence)
	}
	if calls, stepID := pendingToolCalls(rows); len(calls) > 0 {
		for _, row := range rows {
			if row.StepID != nil && *row.StepID == stepID && parseContextMessage(row.Content).Role == "assistant" {
				*through = row.Sequence
				break
			}
		}
		if err := r.executeToolCalls(ctx, stepID, calls); err != nil {
			return modelRequest{}, uuid.Nil, err
		}
	}
	// 在开步骤前确定本轮模型，同一份选择用于步骤、压缩与普通请求。
	settings, err := modelsettings.LoadUser(budget, queries, r.userID)
	if err != nil {
		return modelRequest{}, uuid.Nil, err
	}
	model, capacity := settings.MainModel, settings.MainContextLength
	if r.subagentID != nil && settings.SubagentModel != "" {
		model, capacity = settings.SubagentModel, settings.SubagentContextLength
	}
	var stepID uuid.UUID
	err = pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		txQueries := database.New(tx)
		if err := r.lockAndVerifyLease(ctx, txQueries); err != nil {
			return err
		}
		var err error
		stepID, err = txQueries.StartStep(ctx, database.StartStepParams{
			UserID:     r.userID,
			SubagentID: r.subagentID,
			Model:      model,
		})
		return err
	})
	if err != nil {
		return modelRequest{}, uuid.Nil, err
	}
	request, err := r.prepareContext(budget, model, capacity)
	if err != nil {
		return modelRequest{}, stepID, err
	}
	*through = request.InputSequence
	return request, stepID, nil
}

// prepareContext 是每次调用模型前的上下文入口：按 Main 或 Subagent 身份选择模型与上限，读入当前世代的消息，
// 判断要不要压缩，需要时先完成压缩，再按组装规则返回最终请求。
func (r *Runner) prepareContext(ctx context.Context, model string, capacity int64) (modelRequest, error) {
	queries := database.New(r.databasePool)
	settings, err := modelsettings.LoadRuntime(ctx, queries)
	if err != nil {
		return modelRequest{}, err
	}
	rows, err := queries.ListAgentContextRows(ctx, database.ListAgentContextRowsParams{UserID: r.userID, SubagentID: r.subagentID})
	if err != nil {
		return modelRequest{}, err
	}
	// 触发只看当前 Agent 上一次同模型成功请求的官方 prompt_tokens；没有上次用量或已换模型时按 0 处理。
	var before int64
	usage, err := queries.GetLastContextUsage(ctx, database.GetLastContextUsageParams{UserID: r.userID, SubagentID: r.subagentID, Generation: contextGeneration(rows)})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return modelRequest{}, err
	}
	if err == nil && usage.RequestedModel == model {
		before = int64(usage.InputTokens)
	}
	// 容量保护优先。保留最后一条完整工具链之后的输入，沿用现有容量余量。
	switch {
	case capacity > 0 && before > 0 && before >= capacity-max(1024, capacity/20):
		end := len(rows)
		for end > 0 {
			role := parseContextMessage(rows[end-1].Content).Role
			if role == "assistant" || role == "tool" {
				break
			}
			end--
		}
		if end == 0 {
			return r.buildAgentRequest(rows, model), nil
		}
		if err := r.compactContext(ctx, rows[:end], rows[end-1].Sequence, model, compactPrompt, "compact", nil); err != nil {
			return modelRequest{}, err
		}
	case before >= int64(settings.CompactAtTokens):
		// 当前世代只插入一次；后续请求与压缩重放同一条不可见消息。
		if slices.ContainsFunc(rows, func(row database.ListAgentContextRowsRow) bool {
			return parseContextMessage(row.Content).Name == "compact_reminder"
		}) {
			return r.buildAgentRequest(rows, model), nil
		}
		err := pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
			q := database.New(tx)
			if err := r.lockAndVerifyLease(ctx, q); err != nil {
				return err
			}
			return q.InsertAgentMessage(ctx, database.InsertAgentMessageParams{
				UserID: r.userID, SubagentID: r.subagentID, Role: "user",
				Content: runtimeMessage("compact_reminder", "当前上下文已达到压缩提醒线，请尽量在下一个适合的任务节点调用 compact(reason, focus)。"),
			})
		})
		if err != nil {
			return modelRequest{}, err
		}
	default:
		return r.buildAgentRequest(rows, model), nil
	}
	// 重新读取事务期间到达的新消息，保留它们的原文与序号。
	rows, err = queries.ListAgentContextRows(ctx, database.ListAgentContextRowsParams{UserID: r.userID, SubagentID: r.subagentID})
	if err != nil {
		return modelRequest{}, err
	}
	return r.buildAgentRequest(rows, model), nil
}

// compactContext 供容量保护、显式工具调用与最终回答后的热压缩共用；在当前租约保护下复用旧世代前缀生成摘要并原子提交。
func (r *Runner) compactContext(ctx context.Context, rows []database.ListAgentContextRowsRow, through int64, model, prompt, purpose string, receipt *database.InsertAgentMessageParams) error {
	queries := database.New(r.databasePool)
	request := r.buildAgentRequest(rows, model)
	request.Messages = append(request.Messages, runtimeMessage("compaction", compactInstructions+"\n\n"+prompt))
	var compactStepID uuid.UUID
	err := pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		txQueries := database.New(tx)
		if err := r.lockAndVerifyLease(ctx, txQueries); err != nil {
			return err
		}
		var err error
		compactStepID, err = txQueries.StartCompactStep(ctx, database.StartCompactStepParams{
			UserID:     r.userID,
			SubagentID: r.subagentID,
			Model:      model,
			Purpose:    purpose,
		})
		return err
	})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		failure := "compaction did not commit; original context preserved"
		_ = queries.FinishStep(cleanup, database.FinishStepParams{ID: compactStepID, Error: &failure})
	}()
	result, _, err := r.sendModelRequest(ctx, request, "compact")
	if err != nil {
		return fmt.Errorf("compact context: %w", err)
	}
	message := result.Choices[0].Message
	summary := AssistantTextToUser(message)
	if summary == "" || len(parseContextMessage(message).ToolCalls) > 0 {
		return errors.New("compact response must contain summary text without tool calls")
	}
	summaryJSON, _ := json.Marshal(map[string]any{"role": "user", "name": "context_summary", "content": contextSummaryPrompt + summary})
	err = pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		q := database.New(tx)
		if err := r.lockAndVerifyLease(ctx, q); err != nil {
			return err
		}
		if err := q.InsertContextSummary(ctx, database.InsertContextSummaryParams{
			UserID:                 r.userID,
			SubagentID:             r.subagentID,
			Content:                summaryJSON,
			StepID:                 &compactStepID,
			SummaryThroughSequence: &through,
		}); err != nil {
			return err
		}
		if receipt != nil {
			if err := q.InsertAgentMessage(ctx, *receipt); err != nil {
				return err
			}
		}
		return q.FinishStep(ctx, database.FinishStepParams{
			ID:                compactStepID,
			InputTokens:       result.Usage.PromptTokens,
			CachedInputTokens: result.Usage.CachedTokens,
			OutputTokens:      result.Usage.CompletionTokens,
			Cost:              result.Usage.Cost,
		})
	})
	if err != nil {
		return err
	}
	committed = true
	return nil
}

// silentToolRoundsBeforeReminder 是从最近一条用户消息或 Main 的可见正文算起，连续多少轮只调工具后插入一次进度提醒。
const silentToolRoundsBeforeReminder = 4

// parsedContextMessage 是组装上下文时从一条已存消息里读出的共用字段。
type parsedContextMessage struct {
	Name       string          `json:"name"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []toolCall      `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

// parseContextMessage 解析一条已存消息；内容由写入方保证形状，这里只做组装所需的读取。
func parseContextMessage(raw json.RawMessage) parsedContextMessage {
	var message parsedContextMessage
	_ = json.Unmarshal(raw, &message)
	return message
}

// runtimeMessage 生成一条运行时注入的不可见 user 消息，用 name 标明用途；它只存在于请求里，不落库。
func runtimeMessage(name, content string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"role": "user", "name": name, "content": content})
	return raw
}

// requestMessage 把一条已存消息转成请求中的原生消息：只删前端展示字段（包括 forward_subagent 的 parts），正文、工具调用与渠道原生字段原样保留。
func requestMessage(row database.ListAgentContextRowsRow) json.RawMessage {
	var message map[string]json.RawMessage
	_ = json.Unmarshal(row.Content, &message)
	delete(message, "user_text")
	delete(message, "quotes")
	delete(message, "image_names")
	delete(message, "parts")
	raw, _ := json.Marshal(message)
	return raw
}

// assembleMessages 按组装规则把当前世代的消息排成请求顺序，普通请求与压缩请求共用：
// 每个工具调用的回执紧跟在它的 assistant 之后；Main 在用户本人消息后附时间、连续 4 轮无正文工具调用后插进度提醒，Subagent 不插聊天进度提醒。
func assembleMessages(rows []database.ListAgentContextRowsRow, includeProgressReminders bool) []json.RawMessage {
	results := map[string]json.RawMessage{}
	for _, row := range rows {
		if message := parseContextMessage(row.Content); message.Role == "tool" {
			results[message.ToolCallID] = requestMessage(row)
		}
	}
	messages := []json.RawMessage{}
	silentToolRounds := 0
	for _, row := range rows {
		message := parseContextMessage(row.Content)
		switch message.Role {
		case "tool":
			continue
		case "user":
			messages = append(messages, requestMessage(row))
			if row.Sender != nil && *row.Sender == "user" {
				messages = append(messages, runtimeMessage(currentTimeMessageName, fmt.Sprintf(currentTimePrompt, row.CreatedAt.UTC().Format(time.RFC3339))))
				silentToolRounds = 0
			}
		case "assistant":
			messages = append(messages, requestMessage(row))
			for _, call := range message.ToolCalls {
				messages = append(messages, results[call.ID])
			}
			if !includeProgressReminders {
				continue
			}
			if AssistantTextToUser(row.Content) != "" {
				silentToolRounds = 0
				continue
			}
			if len(message.ToolCalls) == 0 {
				continue
			}
			silentToolRounds++
			if silentToolRounds == silentToolRoundsBeforeReminder {
				messages = append(messages, runtimeMessage(progressReminderMessageName, progressReminder))
				silentToolRounds = 0
			}
		}
	}
	return messages
}

// buildRequestMessages 按身份注入 MainInstructions 或 SubagentInstructions 后展开数据库中的摘要与历史。
func (r *Runner) buildRequestMessages(rows []database.ListAgentContextRowsRow) []json.RawMessage {
	instructions := MainInstructions
	if r.subagentID != nil {
		instructions = SubagentInstructions
	}
	return append([]json.RawMessage{runtimeMessage(instructionsMessageName, instructions)}, assembleMessages(rows, r.subagentID == nil)...)
}

// contextGeneration 以最新摘要序号标识世代，首次压缩前使用 0。
func contextGeneration(rows []database.ListAgentContextRowsRow) int64 {
	if len(rows) > 0 && rows[0].IsSummary {
		return rows[0].Sequence
	}
	return 0
}

// requestSessionID 按 Agent 身份与上下文世代生成稳定的 OpenRouter 会话 ID。
func (r *Runner) requestSessionID(rows []database.ListAgentContextRowsRow) string {
	generation := contextGeneration(rows)
	if r.subagentID != nil {
		return fmt.Sprintf("subagent:%s:%d", r.subagentID.String(), generation)
	}
	return fmt.Sprintf("main:%s:%d", r.userID.String(), generation)
}

// buildAgentRequest 组装固定指令与历史，并附上当前角色允许的工具定义打包成完整请求。
func (r *Runner) buildAgentRequest(rows []database.ListAgentContextRowsRow, model string) modelRequest {
	messages := r.buildRequestMessages(rows)
	var inputSequence int64
	for _, row := range rows {
		inputSequence = max(inputSequence, row.Sequence)
	}
	return modelRequest{
		InputSequence: inputSequence,
		SessionID:     r.requestSessionID(rows),
		Model:         model,
		Messages:      messages,
		Tools:         r.toolDefinitions(),
	}
}

// pendingToolCalls 从消息尾部找出最后一条 assistant 里还没有回执的工具调用，连同产生它的步骤一起返回。
func pendingToolCalls(rows []database.ListAgentContextRowsRow) ([]toolCall, uuid.UUID) {
	answered := map[string]bool{}
	for index := len(rows) - 1; index >= 0; index-- {
		message := parseContextMessage(rows[index].Content)
		if message.Role == "tool" {
			answered[message.ToolCallID] = true
			continue
		}
		if message.Role != "assistant" {
			continue
		}
		pending := []toolCall{}
		for _, call := range message.ToolCalls {
			if !answered[call.ID] {
				pending = append(pending, call)
			}
		}
		return pending, *rows[index].StepID
	}
	return nil, uuid.Nil
}
