// 本文件实现历史检索工具：search_message 在允许范围内对 messages 全表做关键词与向量混合搜索，
// read_message 按 ID 读回一条消息的原文。范围由调用者身份在服务端固定，模型给出的参数不能越界。

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// newVector 把 embedding 接口返回的浮点数组转成 sqlc 期望的向量指针。
func newVector(values []float32) *pgvector.Vector {
	vector := pgvector.NewVector(values)
	return &vector
}

// historyScope 把调用者身份换成查询范围：Main 搜当前用户的全部列表，Subagent 只搜自己那一份。
func historyScope(address agentAddress) (allAgents bool, subagentID *uuid.UUID) {
	if address.subagentID == nil {
		return true, nil
	}
	return false, address.subagentID
}

// historyHit 是一条搜索命中对外返回的形状：标明归属与说话人，模型据此区分用户原话、Agent 回复与摘要。
type historyHit struct {
	MessageID  uuid.UUID  `json:"message_id"`
	Agent      string     `json:"agent"`
	SubagentID *uuid.UUID `json:"subagent_id,omitempty"`
	Role       string     `json:"role"`
	Sender     *string    `json:"sender,omitempty"`
	IsSummary  bool       `json:"is_summary"`
	CreatedAt  time.Time  `json:"created_at"`
	Matched    []string   `json:"matched"`
	Excerpt    string     `json:"excerpt"`

	// score 与 sequence 只用于合并排序，不对外返回。
	score    float64
	sequence int64
}

// executeSearchMessage 执行混合搜索：关键词路找精确命中，向量路找语义相近，两路各取一批后按排名合并去重。
// 向量不可用（未配置或接口失败）时降级为纯关键词结果，并在结果里注明，避免模型以为语义搜索已经查全。
func (r *Runner) executeSearchMessage(ctx context.Context, address agentAddress, arguments string) (string, error) {
	var parsed struct {
		Keyword string `json:"keyword"`
		Query   string `json:"query"`
		Who     string `json:"who"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return "Error: arguments are not valid JSON", nil
	}
	parsed.Keyword = strings.TrimSpace(parsed.Keyword)
	parsed.Query = strings.TrimSpace(parsed.Query)
	if parsed.Keyword == "" && parsed.Query == "" {
		return "Error: keyword or query is required", nil
	}
	if parsed.Who == "" {
		parsed.Who = "any"
	}
	if parsed.Who != "any" && parsed.Who != "user" && parsed.Who != "main" && parsed.Who != "subagent" {
		return "Error: who must be one of any, user, main, subagent", nil
	}
	if parsed.Limit == 0 {
		parsed.Limit = 10
	}
	if parsed.Limit < 1 || parsed.Limit > 20 {
		return "Error: limit must be between 1 and 20", nil
	}
	allAgents, scopeSubagentID := historyScope(address)
	queries := database.New(r.databasePool)
	candidateLimit := int32(max(parsed.Limit*3, 30))
	hits := map[uuid.UUID]*historyHit{}
	// 关键词路：给了 keyword 才走，向量路用 query（没给时拿 keyword 当语义查询）合并补充。
	if parsed.Keyword != "" {
		rows, err := queries.SearchMessagesByKeyword(ctx, database.SearchMessagesByKeywordParams{Keyword: parsed.Keyword, UserID: address.userID, AllAgents: allAgents, SubagentID: scopeSubagentID, Who: parsed.Who, LimitCount: candidateLimit})
		if err != nil {
			return "", err
		}
		for rank, row := range rows {
			hit := &historyHit{MessageID: row.ID, Agent: historyAgentName(row.SubagentID), SubagentID: row.SubagentID, Role: row.Role, Sender: row.Sender, IsSummary: row.IsSummary, CreatedAt: row.CreatedAt, Matched: []string{"keyword"}, Excerpt: row.Excerpt, sequence: row.Sequence}
			hit.score += 1.0 / float64(60+rank+1)
			hits[row.ID] = hit
		}
	}
	semanticText := parsed.Query
	if semanticText == "" {
		semanticText = parsed.Keyword
	}
	semanticAvailable := true
	queryVector, err := r.embedText(ctx, semanticText)
	if err != nil {
		semanticAvailable = false
	} else {
		rows, err := queries.SearchMessagesByVector(ctx, database.SearchMessagesByVectorParams{QueryEmbedding: newVector(queryVector), UserID: address.userID, AllAgents: allAgents, SubagentID: scopeSubagentID, Who: parsed.Who, LimitCount: candidateLimit})
		if err != nil {
			return "", err
		}
		for rank, row := range rows {
			if hit, exists := hits[row.ID]; exists {
				hit.Matched = append(hit.Matched, "vector")
				hit.score += 1.0 / float64(60+rank+1)
				continue
			}
			hit := &historyHit{MessageID: row.ID, Agent: historyAgentName(row.SubagentID), SubagentID: row.SubagentID, Role: row.Role, Sender: row.Sender, IsSummary: row.IsSummary, CreatedAt: row.CreatedAt, Matched: []string{"vector"}, Excerpt: row.Excerpt, sequence: row.Sequence}
			hit.score += 1.0 / float64(60+rank+1)
			hits[row.ID] = hit
		}
	}
	if parsed.Keyword == "" && !semanticAvailable {
		return "Error: semantic search is unavailable because the embedding service is not configured or failed; provide a keyword instead", nil
	}
	// 两路分数单位不同不能直接相加，合并只看各自排名（RRF）：名次越靠前贡献越大，两路都命中自然排到前面。
	ordered := make([]*historyHit, 0, len(hits))
	for _, hit := range hits {
		ordered = append(ordered, hit)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		return ordered[i].sequence > ordered[j].sequence
	})
	if len(ordered) > parsed.Limit {
		ordered = ordered[:parsed.Limit]
	}
	result := struct {
		Results []*historyHit `json:"results"`
		Note    string        `json:"note,omitempty"`
	}{Results: ordered}
	if len(ordered) == 0 {
		result.Note = "no matching messages in the searchable history"
	} else if !semanticAvailable {
		result.Note = "semantic search unavailable; results are keyword matches only"
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

// historyAgentName 把消息归属说成人能读的身份：没有 subagent_id 就是 Main 的列表。
func historyAgentName(subagentID *uuid.UUID) string {
	if subagentID == nil {
		return "main"
	}
	return "subagent"
}

// executeReadMessage 按 ID 读回一条消息的完整提取文本；范围外的 ID 与不存在的 ID 返回同一句错误，不泄露归属。
func executeReadMessage(ctx context.Context, _ *Runner, queries *database.Queries, address agentAddress, arguments string) (string, error) {
	var parsed struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return "Error: arguments are not valid JSON", nil
	}
	messageID, err := uuid.Parse(parsed.MessageID)
	if err != nil {
		return "Error: message_id must be a message UUID returned by search_message", nil
	}
	allAgents, scopeSubagentID := historyScope(address)
	row, err := queries.GetHistoryMessage(ctx, database.GetHistoryMessageParams{UserID: address.userID, AllAgents: allAgents, SubagentID: scopeSubagentID, MessageID: messageID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "Error: message not found in your searchable history", nil
	}
	if err != nil {
		return "", err
	}
	// 极长的消息（多为工具抓回的网页全文）截断返回，避免一次读回就占满上下文；截断会明确标出。
	text := row.SearchText
	truncated := false
	if runes := []rune(text); len(runes) > 32000 {
		text = string(runes[:32000])
		truncated = true
	}
	result := struct {
		MessageID  uuid.UUID  `json:"message_id"`
		Agent      string     `json:"agent"`
		SubagentID *uuid.UUID `json:"subagent_id,omitempty"`
		Role       string     `json:"role"`
		Sender     *string    `json:"sender,omitempty"`
		IsSummary  bool       `json:"is_summary"`
		CreatedAt  time.Time  `json:"created_at"`
		Text       string     `json:"text"`
		Truncated  bool       `json:"truncated,omitempty"`
	}{MessageID: row.ID, Agent: historyAgentName(row.SubagentID), SubagentID: row.SubagentID, Role: row.Role, Sender: row.Sender, IsSummary: row.IsSummary, CreatedAt: row.CreatedAt, Text: text, Truncated: truncated}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
