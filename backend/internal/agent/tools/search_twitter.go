// 本文件实现 search_twitter 工具：复用 EveryInfra 凭证搜索公开推文，保留正文、来源与互动数据供模型引用。

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
)

// searchTwitterTool 登记 search_twitter：联网请求在事务外执行，回执单独提交。
var searchTwitterTool = Tool{
	Name:               "search_twitter",
	Definition:         json.RawMessage(`{"type":"function","function":{"name":"search_twitter","description":"Search public X (Twitter) posts via EveryInfra. Returns post text, source URLs, authors, publication times and available engagement counts. Query supports X search operators such as from:, since: and until:. Defaults to Top results; latest is only a preference and does not guarantee chronological order. Results may be fewer than requested and are not a complete archive. Cite original post URLs; missing counts are unknown, not zero.","parameters":{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":200,"default":25},"sort":{"type":"string","enum":["top","latest"],"default":"top"}},"required":["query"]}}}`),
	OutsideTransaction: true,
	Execute: func(ctx context.Context, deps Dependencies, _ *database.Queries, _ uuid.UUID, arguments string) (string, error) {
		client := newWebClient(deps)
		return client.searchTwitter(ctx, json.RawMessage(arguments))
	},
}

// twitterSearchResult 保留推文的来源与公开指标；可空计数使用指针，避免把未知值当成零。
type twitterSearchResult struct {
	ID                  string `json:"id,omitempty"`
	URL                 string `json:"url,omitempty"`
	Text                string `json:"text,omitempty"`
	PostedAt            string `json:"posted_at,omitempty"`
	AuthorUsername      string `json:"author_username,omitempty"`
	AuthorName          string `json:"author_name,omitempty"`
	AuthorURL           string `json:"author_url,omitempty"`
	AuthorFollowerCount *int64 `json:"author_follower_count,omitempty"`
	LikeCount           *int64 `json:"like_count,omitempty"`
	RetweetCount        *int64 `json:"retweet_count,omitempty"`
	ReplyCount          *int64 `json:"reply_count,omitempty"`
	ViewCount           *int64 `json:"view_count,omitempty"`
}

// searchTwitter 校验模型参数并执行一次同步搜索；失败直接交回模型，不自动重试付费请求。
func (c *webClient) searchTwitter(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Query string `json:"query"`
		Limit *int   `json:"limit"`
		Sort  string `json:"sort"`
	}
	if err := decodeArguments(args, &input); err != nil {
		return "", err
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" {
		return "", errors.New("search_twitter requires a non-empty query")
	}
	if input.Limit != nil && (*input.Limit < 1 || *input.Limit > 200) {
		return "", errors.New("search_twitter limit must be between 1 and 200")
	}
	if input.Sort != "" && input.Sort != "top" && input.Sort != "latest" {
		return "", errors.New("search_twitter sort must be top or latest")
	}

	// top 沿用供应商默认值；latest 只表达排序偏好，不能保证结果严格按发布时间排列。
	params := map[string]any{"keyword": input.Query}
	if input.Limit != nil {
		params["limit"] = *input.Limit
	}
	if input.Sort == "latest" {
		params["sort"] = input.Sort
	}
	credentials, err := c.services.Load(ctx, services.EveryInfra)
	if err != nil {
		return "", err
	}
	requestContext, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	// 指针区分空列表与缺失列表；推文没有文章标题，不按通用搜索的标题规则过滤结果。
	var response struct {
		Results *[]twitterSearchResult `json:"results"`
	}
	if err := c.postJSON(requestContext, "https://api.everyinfra.com/api/v1/social", "Authorization", "Bearer "+credentials.APIKey,
		map[string]any{"platform": "twitter", "action": "search", "params": params}, &response); err != nil {
		return "", err
	}
	if response.Results == nil {
		return "", errors.New("EveryInfra returned an invalid Twitter search response")
	}
	encoded, err := json.Marshal(response)
	return string(encoded), err
}
