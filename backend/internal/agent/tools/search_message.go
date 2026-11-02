// 本文件实现 search_message 工具：对当前用户的 messages 全表做关键词搜索，范围在服务端固定为当前用户，模型给出的参数不能越界。

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
)

// searchMessageTool 登记 search_message：只读搜索走连接池在事务外执行，回执单独提交。
var searchMessageTool = Tool{
	Name:               "search_message",
	Definition:         json.RawMessage(`{"type":"function","function":{"name":"search_message","description":"Search this user's message history in the messages table (all message types) by a literal keyword such as a name, ID or quoted text. who limits the speaker: user, main or any. Returns message IDs, speakers and excerpts; read a result with read_message before relying on its details. If a keyword finds nothing, retry with a shorter or more specific keyword.","parameters":{"type":"object","additionalProperties":false,"properties":{"keyword":{"type":"string","description":"Exact word or phrase to match literally."},"who":{"type":"string","enum":["any","user","main"],"default":"any"},"limit":{"type":"integer","minimum":1,"maximum":20,"default":10}},"required":["keyword"]}}}`),
	OutsideTransaction: true,
	Execute: func(ctx context.Context, deps Dependencies, _ *database.Queries, userID uuid.UUID, arguments string) (string, error) {
		return executeSearchMessage(ctx, deps, userID, arguments)
	},
}

// historyHit 是一条搜索命中对外返回的形状：标明说话人，模型据此区分用户原话、Main 回复与摘要。
type historyHit struct {
	MessageID uuid.UUID `json:"message_id"`
	Role      string    `json:"role"`
	Sender    *string   `json:"sender,omitempty"`
	IsSummary bool      `json:"is_summary"`
	CreatedAt time.Time `json:"created_at"`
	Excerpt   string    `json:"excerpt"`
}

// executeSearchMessage 执行关键词搜索：在 search_text 里找包含关键词的消息，数据库已按命中次数与新旧排好序，这里只截到 limit 条。
func executeSearchMessage(ctx context.Context, deps Dependencies, userID uuid.UUID, arguments string) (string, error) {
	var parsed struct {
		Keyword string `json:"keyword"`
		Who     string `json:"who"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return "Error: arguments are not valid JSON", nil
	}
	parsed.Keyword = strings.TrimSpace(parsed.Keyword)
	if parsed.Keyword == "" {
		return "Error: keyword is required", nil
	}
	if parsed.Who == "" {
		parsed.Who = "any"
	}
	if parsed.Who != "any" && parsed.Who != "user" && parsed.Who != "main" {
		return "Error: who must be one of any, user, main", nil
	}
	if parsed.Limit == 0 {
		parsed.Limit = 10
	}
	if parsed.Limit < 1 || parsed.Limit > 20 {
		return "Error: limit must be between 1 and 20", nil
	}
	queries := database.New(deps.DatabasePool)
	rows, err := queries.SearchMessagesByKeyword(ctx, database.SearchMessagesByKeywordParams{Keyword: parsed.Keyword, UserID: userID, Who: parsed.Who, LimitCount: int32(parsed.Limit)})
	if err != nil {
		return "", err
	}
	hits := make([]*historyHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, &historyHit{MessageID: row.ID, Role: row.Role, Sender: row.Sender, IsSummary: row.IsSummary, CreatedAt: row.CreatedAt, Excerpt: row.Excerpt})
	}
	result := struct {
		Results []*historyHit `json:"results"`
		Note    string        `json:"note,omitempty"`
	}{Results: hits}
	if len(hits) == 0 {
		result.Note = "no matching messages in the searchable history"
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
