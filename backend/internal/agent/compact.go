// 本文件负责上下文准备与压缩：普通请求按原生形态组装当前世代的消息，压缩达到阈值时用一条摘要开启新世代再继续对话；
// 恰好是用户新消息触发的压缩只压这条消息之前的历史，让它以原文接在摘要之后发给模型，不看闲置时长。
// 原始消息始终保留在数据库里，压缩失败就用原上下文，不丢任何内容。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/modelsettings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// prepareContext 是每次调用模型前的上下文入口：组装该 Agent 的完整消息，
// 判断要不要压缩（上次官方用量达到 token 阈值或容量上限；不再看闲置时长），需要时先完成压缩再返回最终请求和消息列表。
// Main 若已连续多轮只调工具、没有用户可见正文，会在发出前插入一条进度提醒。
// 输入按已确定的值信任：模型、容量、阈值都来自设置；压缩只对模型调用本身失败负责，不处理其他假想状态（见 docs/wiki/05-main-runtime.md）。
// newMessageIDs 是 prepareStep 这一步刚领到的待读消息，
// 用来认出哪条是刚到的用户消息；没有它，光看上下文列表分不清新消息和早已被回复过的用户消息。stepID 是这一步的身份，当前时间行按它去重。
func (r *Runner) prepareContext(ctx context.Context, connection *pgxpool.Conn, userID uuid.UUID, stepID uuid.UUID, newMessageIDs []uuid.UUID) (modelRequest, []json.RawMessage, error) {
	// 先定这次用哪个模型和上下文上限：取管理员在看板保存的设置。
	queries := database.New(connection)
	settings, err := modelsettings.Load(ctx, queries, r.settings)
	if err != nil {
		return modelRequest{}, nil, err
	}
	model, outputLimit, capacity := settings.MainModel, settings.MainOutputLimit, settings.MainContextLength
	// 读入该 Agent 的全部上下文行，供下面组装完整消息与判断待压缩范围用。
	rows, err := loadContextRows(ctx, queries, userID)
	if err != nil {
		return modelRequest{}, nil, err
	}
	messages := rawContextMessages(rows)
	// 保留原文只认 Main 这一步刚领到的用户消息：从后往前找到它，并记下它之前还有没有没被摘要替代的原文。
	// 它之前只剩上一条摘要（同一轮重试时摘要已落库）或没有任何消息（首条消息）时，没有可压的原文，
	// 即使上次用量还停在阈值之上也不能把这条消息连同摘要再压一遍。
	newUserMessageIndex := -1
	newUserHistoryHasOriginal := false
	if len(newMessageIDs) > 0 {
		justArrived := make(map[uuid.UUID]bool, len(newMessageIDs))
		for _, id := range newMessageIDs {
			justArrived[id] = true
		}
		for index := len(rows) - 1; index >= 0; index-- {
			row := rows[index]
			if row.IsSummary || row.Sender == nil || *row.Sender != "user" || !justArrived[row.ID] {
				continue
			}
			newUserMessageIndex = index
			for _, historyRow := range rows[:index] {
				// 摘要与世代开头的指令行都不算可再压的对话原文。
				if !historyRow.IsSummary && parseContextMessage(historyRow.Content).Name != instructionsMessageName {
					newUserHistoryHasOriginal = true
					break
				}
			}
			break
		}
	}
	// 触发只看官方用量：上一次成功的 Agent 请求由 OpenRouter 回的 prompt_tokens 已存入 model_calls，直接拿它当压缩前的大小；本地不再估算。没有上次用量（首次请求）或已换模型（不同 tokenizer 的数字不可比）时按 0 处理，不因 token 或容量触发。
	var before int64
	usage, err := queries.GetLastContextUsage(ctx, userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return modelRequest{}, nil, err
	}
	if err == nil && usage.RequestedModel == model {
		before = int64(usage.InputTokens)
	}
	// 算出输入能用多少：总容量先扣掉给模型输出预留的部分和一成余量，剩下的才是历史能占的上限；再看是撞到压缩阈值还是已经超容量。
	inputLimit := int64(math.MaxInt32)
	if capacity > 0 {
		inputLimit = capacity - int64(outputLimit) - max(1024, capacity/20)
		// Gemini 目录给的是独立输入上限，不像 OpenRouter 总上下文容量那样扣除输出预算。
		if strings.HasPrefix(model, "ikun:gemini-") {
			inputLimit = capacity - max(1024, capacity/20)
		}
	}
	// ikun Gemini 的输入上限远大于看板通用阈值，固定到 400,000 才压缩（2026-10-30 用户确认），晚一点丢历史细节；其他渠道仍用看板阈值。
	compactAt := int64(settings.CompactAtTokens)
	if strings.HasPrefix(model, "ikun:gemini-") {
		compactAt = 400000
	}
	threshold := min(compactAt, inputLimit)
	reason := ""
	if before >= threshold {
		reason = "tokens"
	}
	if before >= inputLimit {
		reason = "capacity"
	}
	// 待压缩范围默认是全部上下文；达到阈值且这一步恰好领到用户新消息时，只压它之前的历史，
	// 它与同时领到的其余消息不进压缩请求、原文保留，没达到阈值则不压缩、不再看闲置了多久。
	rowsToCompact := rows
	keepNewUserMessage := newUserMessageIndex >= 0 && newUserHistoryHasOriginal && reason != ""
	if keepNewUserMessage {
		reason = "user_message"
		rowsToCompact = rows[:newUserMessageIndex]
	}
	// 没达到阈值就不压缩；刚领到的用户消息之前没有原文时也不压缩（见上面新消息一段的说明）。
	// 不压缩时先按需把当前时间落库，再按原生形态组装；发出前仍可能为 Main 补一条进度提醒。
	if reason == "" || (newUserMessageIndex >= 0 && !newUserHistoryHasOriginal) {
		if len(newMessageIDs) > 0 {
			if rows, err = r.ensureCurrentTimeMessage(ctx, connection, userID, stepID, rows); err != nil {
				return modelRequest{}, nil, err
			}
		}
		messages = rawContextMessages(rows)
		request, err := r.agentRequest(rows, model, outputLimit)
		if err != nil {
			return modelRequest{}, nil, err
		}
		request.Messages, err = r.appendProgressReminder(ctx, connection, userID, messages, request.Messages)
		if err != nil {
			return modelRequest{}, nil, err
		}
		return request, messages, nil
	}
	// 组装压缩请求：压缩指令作为不可见 user 消息在前，待压历史保持原生形态，摘要要求作为最后一条 user 消息；
	// 模型返回的摘要开启新世代，保留下来的新消息原文接在新世代摘要之后。
	// 压缩只许产出摘要正文：请求明确禁止调用任何工具，避免模型顺着历史里的工具轨迹继续干活而不是写摘要。
	compactMessages := buildCompactRequestMessages(rowsToCompact, compactPrompt+fmt.Sprintf(compactSummaryLengthPrompt, max(128, min(2048, threshold/8))))

	summaryRequest := modelRequest{Model: model, Messages: compactMessages, MaxTokens: outputLimit, ToolChoice: json.RawMessage(`"none"`), Reasoning: map[string]any{"effort": "low", "exclude": true}}
	// 摘要覆盖位置停在待压缩历史里最后一条原文上：旧摘要自身的位置不算原文，它的覆盖范围由它自己的覆盖位置记录。
	through := rows[len(rows)-1].Sequence
	if keepNewUserMessage {
		for index := newUserMessageIndex - 1; index >= 0; index-- {
			if !rows[index].IsSummary && parseContextMessage(rows[index].Content).Name != instructionsMessageName {
				through = rows[index].Sequence
				break
			}
		}
	}
	// 先登记一个压缩步骤再调模型，方便看板按步骤查这次压缩的原因和前后大小；下面这段收尾保证没提交成功就把它记为失败。
	compactStepID, err := queries.StartCompactStep(ctx, database.StartCompactStepParams{UserID: userID, Model: model})
	if err != nil {
		return modelRequest{}, nil, err
	}
	committed := false
	defer func() {
		if committed || connection.Conn().IsClosed() {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		failure := "compaction did not commit; original context preserved"
		_ = queries.FinishStep(cleanup, database.FinishStepParams{ID: compactStepID, Error: &failure})
	}()
	// 真正调用压缩模型，等待期间同样靠 whileRequest 看住执行锁连接。
	beforeValue := int32(min(before, math.MaxInt32))
	result, err := whileRequest(ctx, connection, func(requestContext context.Context) (chatResponse, error) {
		response, _, requestErr := r.requestModel(requestContext, summaryRequest, database.StartModelCallParams{StepID: compactStepID, Purpose: "compact", CompactReason: &reason, ContextTokensBefore: &beforeValue, SummaryThroughSequence: &through})
		return response, requestErr
	})
	if err != nil {
		return modelRequest{}, nil, fmt.Errorf("compact context: %w", err)
	}
	// 调用成功就直接用模型返回的正文作摘要，不再校验内容是否合格。
	var summary struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(result.Choices[0].Message, &summary)
	// 压缩后的新世代以这条摘要替代被压的历史，保留下来的新消息原文依次跟在后面；指令由每次请求的第一条注入，不随世代落库。
	summaryJSON, _ := json.Marshal(map[string]any{"role": "user", "name": "context_summary", "content": contextSummaryPrompt + summary.Content})
	// 摘要落库：一个事务里写入摘要消息并给压缩步骤记上用量，两步要么一起成要么都不算；压缩后的大小不估算，下一次 Agent 请求的官方 prompt_tokens 就是它。
	err = pgx.BeginFunc(ctx, connection, func(tx pgx.Tx) error {
		q := database.New(tx)
		if err := q.LockUserMessages(ctx, userID); err != nil {
			return err
		}
		if err := q.InsertContextSummary(ctx, database.InsertContextSummaryParams{UserID: userID, Content: summaryJSON, StepID: &compactStepID, SummaryThroughSequence: &through}); err != nil {
			return err
		}
		return q.FinishStep(ctx, database.FinishStepParams{ID: compactStepID, InputTokens: result.Usage.PromptTokens, OutputTokens: result.Usage.CompletionTokens, Cost: result.Usage.Cost})
	})
	if err != nil {
		return modelRequest{}, nil, err
	}
	committed = true
	// 重读上下文行带上新摘要与保留的新消息原文，再按需追加当前时间。
	rows, err = loadContextRows(ctx, queries, userID)
	if err != nil {
		return modelRequest{}, nil, err
	}
	if len(newMessageIDs) > 0 {
		if rows, err = r.ensureCurrentTimeMessage(ctx, connection, userID, stepID, rows); err != nil {
			return modelRequest{}, nil, err
		}
	}
	messages = rawContextMessages(rows)
	request, err := r.agentRequest(rows, model, outputLimit)
	if err != nil {
		return modelRequest{}, nil, err
	}
	// 提醒按压缩前的原文计数，但只追加在新世代请求之后：它是给接下来这次请求的提示，不应被写进摘要。
	request.Messages, err = r.appendProgressReminder(ctx, connection, userID, rawContextMessages(rowsToCompact), request.Messages)
	if err != nil {
		return modelRequest{}, nil, err
	}
	// 压缩期间新到的消息留给下一轮：prepareStep 领走后，prepareContext 会读到摘要加其后的消息，不会丢。
	return request, messages, nil
}
