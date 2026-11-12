// 本文件是 agent loop 的请求前阶段：按 Main 或 Subagent 隔离收拾上次中断残留、补跑缺回执的工具，
// 再读对应角色模型设置、必要时压缩上下文，并组装这一次的模型请求。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// beforeRequest 是请求前阶段的入口：把当前 Agent 的数据库状态整理到可以发请求，开一条步骤，再产出这一次的请求。
// through 随本轮实际读到的消息更新，失败时由请求后阶段据此停住；准备失败时已开的步骤交给失败收尾结束。
func (r *Runner) beforeRequest(ctx, budget context.Context, through *int64, taskReminder bool) (modelRequest, uuid.UUID, error) {
	queries := database.New(r.databasePool)
	// 在校验本轮租约代数的短事务内收拾上次中断留下的记录，防止已失去租约的旧实例误清接管实例正在运行的调用与步骤；
	// 必须排在开新步骤之前，否则新步骤也会被标成中断。
	err := pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		txQueries := database.New(tx)
		if err := r.lockAndVerifyLease(ctx, txQueries); err != nil {
			return err
		}
		if err := txQueries.InterruptModelCalls(ctx, database.InterruptModelCallsParams{UserID: r.userID, SubagentID: r.subagentID}); err != nil {
			return fmt.Errorf("recover model call journal: %w", err)
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
			Model:      r.settings.MainModel,
		})
		return err
	})
	if err != nil {
		return modelRequest{}, uuid.Nil, err
	}
	request, err := r.prepareContext(budget, taskReminder)
	if err != nil {
		return modelRequest{}, stepID, err
	}
	*through = request.InputSequence
	return request, stepID, nil
}

// prepareContext 是每次调用模型前的上下文入口：按 Main 或 Subagent 身份选择模型与上限，读入当前世代的消息，
// 判断要不要压缩，需要时先完成压缩，再按组装规则返回最终请求。
func (r *Runner) prepareContext(ctx context.Context, taskReminder bool) (modelRequest, error) {
	queries := database.New(r.databasePool)
	settings, err := modelsettings.Load(ctx, queries, r.settings)
	if err != nil {
		return modelRequest{}, err
	}
	model, capacity := settings.MainModel, settings.MainContextLength
	if r.subagentID != nil && settings.SubagentModel != "" {
		model, capacity = settings.SubagentModel, settings.SubagentContextLength
	}
	rows, err := queries.ListAgentContextRows(ctx, database.ListAgentContextRowsParams{UserID: r.userID, SubagentID: r.subagentID})
	if err != nil {
		return modelRequest{}, err
	}
	// 触发只看当前 Agent 上一次同模型成功请求的官方 prompt_tokens；没有上次用量或已换模型时按 0 处理。
	var before int64
	usage, err := queries.GetLastContextUsage(ctx, database.GetLastContextUsageParams{UserID: r.userID, SubagentID: r.subagentID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return modelRequest{}, err
	}
	if err == nil && usage.RequestedModel == model {
		before = int64(usage.InputTokens)
	}
	inputLimit := int64(math.MaxInt32)
	if capacity > 0 {
		inputLimit = capacity - max(1024, capacity/20)
	}
	threshold := min(int64(settings.CompactAtTokens), inputLimit)
	reason := ""
	if before >= threshold {
		reason = "tokens"
	}
	if before >= inputLimit {
		reason = "capacity"
	}
	if reason == "" {
		return r.buildAgentRequest(rows, model, taskReminder), nil
	}
	// 待压缩范围：Main 消息尾部有未回复的用户本人新消息时保留该段原文；工具续跑或 Subagent 全量压缩。
	keepFrom := len(rows)
	for keepFrom > 0 && !rows[keepFrom-1].IsSummary && parseContextMessage(rows[keepFrom-1].Content).Role == "user" {
		keepFrom--
	}
	keepsUserMessage := false
	for _, row := range rows[keepFrom:] {
		if row.Sender != nil && *row.Sender == "user" {
			keepsUserMessage = true
		}
	}
	if keepsUserMessage {
		reason = "user_message"
	} else {
		keepFrom = len(rows)
	}
	rowsToCompact := rows[:keepFrom]
	var through int64
	for _, row := range rowsToCompact {
		if !row.IsSummary {
			through = max(through, row.Sequence)
		}
	}
	if through == 0 {
		return r.buildAgentRequest(rows, model, taskReminder), nil
	}
	compactModel := model
	if settings.CompactModel != "" {
		compactModel = settings.CompactModel
	}
	compactMessages := r.buildCompactRequestMessages(rowsToCompact, compactPrompt)
	summaryRequest := modelRequest{
		Model:      compactModel,
		Messages:   compactMessages,
		Tools:      r.toolDefinitions(),
		ToolChoice: json.RawMessage(`"none"`),
		Reasoning:  map[string]any{"effort": "low", "exclude": true},
	}
	var compactStepID uuid.UUID
	err = pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		txQueries := database.New(tx)
		if err := r.lockAndVerifyLease(ctx, txQueries); err != nil {
			return err
		}
		var err error
		compactStepID, err = txQueries.StartCompactStep(ctx, database.StartCompactStepParams{
			UserID:     r.userID,
			SubagentID: r.subagentID,
			Model:      compactModel,
		})
		return err
	})
	if err != nil {
		return modelRequest{}, err
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
	beforeValue := int32(min(before, math.MaxInt32))
	result, _, err := r.sendModelRequest(ctx, summaryRequest, database.StartModelCallParams{
		StepID:                 compactStepID,
		Purpose:                "compact",
		CompactReason:          &reason,
		ContextTokensBefore:    &beforeValue,
		SummaryThroughSequence: &through,
	}, nil)
	if err != nil {
		return modelRequest{}, fmt.Errorf("compact context: %w", err)
	}
	var summary struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(result.Choices[0].Message, &summary)
	summaryJSON, _ := json.Marshal(map[string]any{"role": "user", "name": "context_summary", "content": contextSummaryPrompt + summary.Content})
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
		return q.FinishStep(ctx, database.FinishStepParams{ID: compactStepID, InputTokens: result.Usage.PromptTokens, OutputTokens: result.Usage.CompletionTokens, Cost: result.Usage.Cost})
	})
	if err != nil {
		return modelRequest{}, err
	}
	committed = true
	rows, err = queries.ListAgentContextRows(ctx, database.ListAgentContextRowsParams{UserID: r.userID, SubagentID: r.subagentID})
	if err != nil {
		return modelRequest{}, err
	}
	return r.buildAgentRequest(rows, model, taskReminder), nil
}

// silentToolRoundsBeforeReminder 是从最近一条用户消息或 Main 的可见正文算起，连续多少轮只调工具后插入一次进度提醒。
const silentToolRoundsBeforeReminder = 4

// parsedContextMessage 是组装上下文时从一条已存消息里读出的共用字段。
type parsedContextMessage struct {
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

// buildAgentRequest 组装固定指令与历史，并附上当前角色允许的工具定义打包成完整请求。
func (r *Runner) buildAgentRequest(rows []database.ListAgentContextRowsRow, model string, taskReminder bool) modelRequest {
	messages := r.buildRequestMessages(rows)
	if taskReminder && r.subagentID == nil {
		messages = append(messages, runtimeMessage(taskReminderMessageName, taskIncompleteReminder))
	}
	var inputSequence int64
	for _, row := range rows {
		inputSequence = max(inputSequence, row.Sequence)
	}
	return modelRequest{InputSequence: inputSequence, Model: model, Messages: messages, Tools: r.toolDefinitions()}
}

// buildCompactRequestMessages 复用当前角色的固定指令和历史前缀，只在末尾追加压缩要求。
func (r *Runner) buildCompactRequestMessages(rows []database.ListAgentContextRowsRow, summaryPrompt string) []json.RawMessage {
	return append(r.buildRequestMessages(rows), runtimeMessage("compaction", compactInstructions+"\n\n"+summaryPrompt))
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
