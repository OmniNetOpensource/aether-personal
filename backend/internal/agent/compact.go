// 本文件负责上下文准备与压缩：普通请求按组装规则排好当前世代的消息，压缩达到阈值时用一条摘要开启新世代再继续对话；
// 消息尾部是还没回复的用户消息时只压它之前的历史，让它以原文接在摘要之后发给模型，不看闲置时长。
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

// prepareContext 是每次调用模型前的上下文入口：读入该 Main 当前世代的消息，
// 判断要不要压缩（上次官方用量达到 token 阈值或容量上限；不看闲置时长），需要时先完成压缩，再按组装规则返回最终请求。
// 输入按已确定的值信任：模型、容量、阈值都来自设置；压缩只对模型调用本身失败负责，不处理其他假想状态（见 docs/wiki/05-main-runtime.md）。
func (r *Runner) prepareContext(ctx context.Context, connection *pgxpool.Conn, userID uuid.UUID) (modelRequest, error) {
	// 先定这次用哪个模型和上下文上限：取管理员在看板保存的设置。
	queries := database.New(connection)
	settings, err := modelsettings.Load(ctx, queries, r.settings)
	if err != nil {
		return modelRequest{}, err
	}
	model, outputLimit, capacity := settings.MainModel, settings.EffectiveOutputLimit, settings.MainContextLength
	rows, err := queries.ListAgentContextRows(ctx, userID)
	if err != nil {
		return modelRequest{}, err
	}
	// 触发只看官方用量：上一次成功的 Agent 请求由供应商回的 prompt_tokens 已存入 model_calls，直接拿它当压缩前的大小；本地不再估算。没有上次用量（首次请求）或已换模型（不同 tokenizer 的数字不可比）时按 0 处理，不因 token 或容量触发。
	var before int64
	usage, err := queries.GetLastContextUsage(ctx, userID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return modelRequest{}, err
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
		return buildAgentRequest(rows, model, outputLimit)
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
	rowsToCompact := rows[:keepFrom]
	// 待压范围里没有原文、只剩上一条摘要（同一轮重试时摘要已落库）时，不把摘要再压一遍。
	if len(rowsToCompact) == 0 || (len(rowsToCompact) == 1 && rowsToCompact[0].IsSummary) {
		return buildAgentRequest(rows, model, outputLimit)
	}
	// 组装压缩请求：压缩指令作为不可见 user 消息在前，待压历史按同一组装规则居中，摘要要求作为最后一条 user 消息；
	// 压缩只许产出摘要正文：请求明确禁止调用任何工具，避免模型顺着历史里的工具轨迹继续干活而不是写摘要。
	compactMessages := buildCompactRequestMessages(rowsToCompact, compactPrompt)
	summaryRequest := modelRequest{Model: model, Messages: compactMessages, MaxTokens: outputLimit, ToolChoice: json.RawMessage(`"none"`), Reasoning: map[string]any{"effort": "low", "exclude": true}}
	// 摘要覆盖位置停在待压缩历史里最后一条原文上；待压范围按序号排列，最后一条就是序号最大的原文。
	through := rowsToCompact[len(rowsToCompact)-1].Sequence
	// 先登记一个压缩步骤再调模型，方便看板按步骤查这次压缩的原因和前后大小；下面这段收尾保证没提交成功就把它记为失败。
	compactStepID, err := queries.StartCompactStep(ctx, database.StartCompactStepParams{UserID: userID, Model: model})
	if err != nil {
		return modelRequest{}, err
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
	result, err := whileRequest(ctx, connection, nil, func(requestContext context.Context, _ func() error) (chatResponse, error) {
		response, _, requestErr := r.sendModelRequest(requestContext, summaryRequest, database.StartModelCallParams{StepID: compactStepID, Purpose: "compact", CompactReason: &reason, ContextTokensBefore: &beforeValue, SummaryThroughSequence: &through}, nil)
		return response, requestErr
	})
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
		return modelRequest{}, err
	}
	committed = true
	// 重读上下文带上新摘要与保留的新消息原文；压缩期间新到的消息序号更大，同样在新世代里。
	rows, err = queries.ListAgentContextRows(ctx, userID)
	if err != nil {
		return modelRequest{}, err
	}
	return buildAgentRequest(rows, model, outputLimit)
}
