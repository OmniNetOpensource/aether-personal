// 本文件是 agent loop 的请求前阶段：从数据库现状出发，收拾上次进程退出留下的记录、补跑缺回执的工具，
// 再读设置、必要时压缩上下文，把消息组装成这一次的模型请求；固定指令、时间与提醒在组装时现算，不改写已保存的历史。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/agent/tools"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// beforeRequest 是请求前阶段的入口：把数据库状态整理到可以发请求，开一条步骤，再产出这一次的请求。
// through 随本轮实际读到的消息更新，失败时由请求后阶段据此停住；准备失败时已开的步骤交给失败收尾结束。
func (r *Runner) beforeRequest(ctx, budget context.Context, through *int64, taskReminder bool) (modelRequest, uuid.UUID, error) {
	queries := database.New(r.databasePool)
	// 收拾上次进程退出留下的记录：进行中的模型调用记为中断，删掉未发布的占位，未结束的步骤以中断收尾。
	// 同一用户只有当前 Runner，且它上一轮的记录在这里都已结束，所以只会清到残留；必须排在开新步骤之前，否则新步骤也会被标成中断。
	if err := queries.InterruptModelCalls(ctx, r.userID); err != nil {
		return modelRequest{}, uuid.Nil, fmt.Errorf("recover model call journal: %w", err)
	}
	if err := queries.DiscardGeneratingReply(ctx, r.userID); err != nil {
		return modelRequest{}, uuid.Nil, fmt.Errorf("recover incomplete reply: %w", err)
	}
	interrupted := "interrupted before the step finished"
	if err := queries.FailUnfinishedSteps(ctx, database.FailUnfinishedStepsParams{UserID: r.userID, Error: &interrupted}); err != nil {
		return modelRequest{}, uuid.Nil, fmt.Errorf("recover unfinished steps: %w", err)
	}
	// 进程中断时可能停在一批工具中间：先补跑没有回执的工具，请求里才不会出现没有回执的调用。
	// 失败只停住本次读到的工作；工具批次以发起它的助手消息为界。
	rows, err := queries.ListAgentContextRows(ctx, r.userID)
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
	stepID, err := queries.StartStep(ctx, database.StartStepParams{UserID: r.userID, Model: r.settings.MainModel})
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

// prepareContext 是每次调用模型前的上下文入口：读入该 Main 当前世代的消息，
// 判断要不要压缩（上次官方用量达到 token 阈值或容量上限；不看闲置时长），需要时先完成压缩，再按组装规则返回最终请求。
// 压缩请求保留普通请求的指令、历史前缀和工具定义，在末尾要求摘要，再开启新世代继续对话；可使用独立模型，原始消息始终保留。
// taskReminder 只追加到最终的 Agent 请求，压缩请求本身不带提醒。
// 输入按已确定的值信任：模型、容量、阈值都来自设置；压缩只对模型调用本身失败负责，不处理其他假想状态（见 docs/wiki/05-main-runtime.md）。
func (r *Runner) prepareContext(ctx context.Context, taskReminder bool) (modelRequest, error) {
	// 先定这次用哪个模型和上下文上限：取管理员在看板保存的设置。
	queries := database.New(r.databasePool)
	settings, err := modelsettings.Load(ctx, queries, r.settings)
	if err != nil {
		return modelRequest{}, err
	}
	model, capacity := settings.MainModel, settings.MainContextLength
	rows, err := queries.ListAgentContextRows(ctx, r.userID)
	if err != nil {
		return modelRequest{}, err
	}
	// 触发只看官方用量：上一次成功的 Agent 请求由供应商回的 prompt_tokens 已存入 model_calls，直接拿它当压缩前的大小；本地不再估算。没有上次用量（首次请求）或已换模型（不同 tokenizer 的数字不可比）时按 0 处理，不因 token 或容量触发。
	var before int64
	usage, err := queries.GetLastContextUsage(ctx, r.userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return modelRequest{}, err
	}
	if err == nil && usage.RequestedModel == model {
		before = int64(usage.InputTokens)
	}
	// 输入容量保留 5% 余量：OpenRouter 不再显式预留最大输出预算，Gemini 目录本身给的就是独立输入上限。
	inputLimit := int64(math.MaxInt32)
	if capacity > 0 {
		inputLimit = capacity - max(1024, capacity/20)
	}
	// 所有模型共用看板阈值（2026-11-03 用户确认取消 ikun Gemini 固定 400,000 的例外）；容量上限仍可更早触发。
	threshold := min(int64(settings.CompactAtTokens), inputLimit)
	reason := ""
	if before >= threshold {
		reason = "tokens"
	}
	if before >= inputLimit {
		reason = "capacity"
	}
	if reason == "" {
		return buildAgentRequest(rows, model, taskReminder), nil
	}
	// 待压缩范围：消息尾部连续的一段非摘要 user 消息里有用户发来的消息时，这段是还没回复的新输入，保留原文，只压它之前的历史；
	// 否则（工具续跑）全部压缩。按序号取尾部，所以保留的消息序号都大于摘要覆盖位置。
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
	// 只有实际对话计入压缩覆盖位置；没有原文、只剩上一条摘要时，不把刚生成的摘要再压一遍。
	rowsToCompact := rows[:keepFrom]
	var through int64
	for _, row := range rowsToCompact {
		if !row.IsSummary {
			through = max(through, row.Sequence)
		}
	}
	if through == 0 {
		return buildAgentRequest(rows, model, taskReminder), nil
	}
	// 独立模型只用于本次摘要，触发阈值和摘要后的主对话仍由 Main 模型决定。
	compactModel := model
	if settings.CompactModel != "" {
		compactModel = settings.CompactModel
	}
	// 工具定义与普通请求相同，由各渠道按同一套转换发出，前缀因此与普通请求一致；tool_choice=none 只禁止调用，不删除定义。
	compactMessages := buildCompactRequestMessages(rowsToCompact, compactPrompt)
	summaryRequest := modelRequest{Model: compactModel, Messages: compactMessages, Tools: tools.Definitions(), ToolChoice: json.RawMessage(`"none"`), Reasoning: map[string]any{"effort": "low", "exclude": true}}
	// 先登记一个压缩步骤再调模型，方便看板按步骤查这次压缩的原因和前后大小；下面这段收尾保证没提交成功就把它记为失败。
	compactStepID, err := queries.StartCompactStep(ctx, database.StartCompactStepParams{UserID: r.userID, Model: compactModel})
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
	// 压缩直接使用本轮上下文调用模型，等待网络期间不持有连接。
	beforeValue := int32(min(before, math.MaxInt32))
	result, _, err := r.sendModelRequest(ctx, summaryRequest, database.StartModelCallParams{StepID: compactStepID, Purpose: "compact", CompactReason: &reason, ContextTokensBefore: &beforeValue, SummaryThroughSequence: &through}, nil)
	if err != nil {
		return modelRequest{}, fmt.Errorf("compact context: %w", err)
	}
	// 调用成功就直接用模型返回的正文作摘要，不再校验内容是否合格。
	var summary struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(result.Choices[0].Message, &summary)
	// 压缩后的新世代以这条摘要替代被压的历史，保留下来的新消息原文依次跟在后面；指令由每次请求的第一条注入，不随世代落库。
	summaryJSON, _ := json.Marshal(map[string]any{"role": "user", "name": "context_summary", "content": contextSummaryPrompt + summary.Content})
	// 摘要落库：一个事务里写入摘要消息并给压缩步骤记上用量，两步要么一起成要么都不算；压缩后的大小不估算，下一次 Agent 请求的官方 prompt_tokens 就是它。
	err = pgx.BeginFunc(ctx, r.databasePool, func(tx pgx.Tx) error {
		q := database.New(tx)
		if err := q.LockUserMessages(ctx, r.userID); err != nil {
			return err
		}
		if err := q.InsertContextSummary(ctx, database.InsertContextSummaryParams{UserID: r.userID, Content: summaryJSON, StepID: &compactStepID, SummaryThroughSequence: &through}); err != nil {
			return err
		}
		return q.FinishStep(ctx, database.FinishStepParams{ID: compactStepID, InputTokens: result.Usage.PromptTokens, OutputTokens: result.Usage.CompletionTokens, Cost: result.Usage.Cost})
	})
	if err != nil {
		return modelRequest{}, err
	}
	committed = true
	// 重读上下文带上新摘要与保留的新消息原文；压缩期间新到的消息序号更大，同样在新世代里。
	rows, err = queries.ListAgentContextRows(ctx, r.userID)
	if err != nil {
		return modelRequest{}, err
	}
	return buildAgentRequest(rows, model, taskReminder), nil
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

// requestMessage 把一条已存消息转成请求中的原生消息：只删前端展示字段，正文、工具调用与渠道原生字段原样保留。
func requestMessage(row database.ListAgentContextRowsRow) json.RawMessage {
	var message map[string]json.RawMessage
	_ = json.Unmarshal(row.Content, &message)
	// 界面引用位置和文件名只用于历史展示，不发送给模型供应商。
	delete(message, "user_text")
	delete(message, "quotes")
	delete(message, "image_names")
	delete(message, "parts")
	raw, _ := json.Marshal(message)
	return raw
}

// assembleMessages 按组装规则把当前世代的消息排成请求顺序，普通请求与压缩请求共用：
// 每个工具调用的回执紧跟在它的 assistant 之后（工具执行期间到达的用户消息因此排在回执之后），
// 每条用户发来的消息后接当时的时间；连续每满 4 轮没有可见正文的工具调用后插一条进度提醒。
func assembleMessages(rows []database.ListAgentContextRowsRow) []json.RawMessage {
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
			// 时间取这条消息的入库时间，同一段历史每次组装仍完全相同。
			if row.Sender != nil && *row.Sender == "user" {
				messages = append(messages, runtimeMessage(currentTimeMessageName, fmt.Sprintf(currentTimePrompt, row.CreatedAt.UTC().Format(time.RFC3339))))
				silentToolRounds = 0
			}
		case "assistant":
			messages = append(messages, requestMessage(row))
			for _, call := range message.ToolCalls {
				messages = append(messages, results[call.ID])
			}
			// Main 写了可见正文就重新计数；只有没有正文的工具轮才累计，满 4 轮插一次提醒。
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

// buildRequestMessages 注入固定指令后，直接展开数据库中的摘要与历史，不额外读取或注入记忆文件。
func buildRequestMessages(rows []database.ListAgentContextRowsRow) []json.RawMessage {
	return append([]json.RawMessage{runtimeMessage(instructionsMessageName, MainInstructions)}, assembleMessages(rows)...)
}

// buildAgentRequest 组装固定指令与历史，并附上全部工具定义打包成完整请求。
// taskReminder 为 true 时这一轮由收尾提醒触发，在历史末尾追加一条任务提醒请模型先核对任务状态。
func buildAgentRequest(rows []database.ListAgentContextRowsRow, model string, taskReminder bool) modelRequest {
	messages := buildRequestMessages(rows)
	// 任务提醒只存在于这一轮请求里，不落库、不改写已有历史，历史前缀保持逐字不变。
	if taskReminder {
		messages = append(messages, runtimeMessage(taskReminderMessageName, taskIncompleteReminder))
	}
	// 只取实际组装的这批行的最大序号；摘要排在首位，其物理序号可能大于保留的原文。
	var inputSequence int64
	for _, row := range rows {
		inputSequence = max(inputSequence, row.Sequence)
	}
	return modelRequest{InputSequence: inputSequence, Model: model, Messages: messages, Tools: tools.Definitions()}
}

// buildCompactRequestMessages 复用普通请求的固定指令和历史前缀，包括已读取的工具结果，只在末尾追加压缩要求。
func buildCompactRequestMessages(rows []database.ListAgentContextRowsRow, summaryPrompt string) []json.RawMessage {
	return append(buildRequestMessages(rows), runtimeMessage("compaction", compactInstructions+"\n\n"+summaryPrompt))
}

// pendingToolCalls 从消息尾部找出最后一条 assistant 里还没有回执的工具调用，连同产生它的步骤一起返回；
// 请求前阶段用它补跑进程中断时没跑完的那批工具，失败收尾用它给这些调用补中断回执。
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
