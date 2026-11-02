// 本文件实现 search 工具：按平台调用 Serper 或 EveryInfra 搜索 Google、微信公众号与小红书，并把结果裁剪成模型可用的标题、链接与摘要。

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

// searchTool 登记 search：联网请求在事务外执行，回执单独提交。
var searchTool = Tool{
	Name:               "search",
	Definition:         json.RawMessage(`{"type":"function","function":{"name":"search","description":"Search Google, Weixin Official Account articles or RedNote notes. Returns titles, URLs and descriptions. Read relevant pages with fetch_url before using them as evidence.","parameters":{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string"},"platform":{"type":"string","enum":["google","weixin","rednote"],"default":"google"}},"required":["query"]}}}`),
	OutsideTransaction: true,
	Execute: func(ctx context.Context, deps Dependencies, _ *database.Queries, _ uuid.UUID, arguments string) (string, error) {
		client := newWebClient(deps)
		return client.search(ctx, json.RawMessage(arguments))
	},
}

// webSearchResult 是 search 对外返回的一条结果：标题、链接与摘要。
type webSearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// search 按平台分发搜索：google 走 Serper，weixin 与 rednote 走 EveryInfra 的社媒接口，平台名不对直接报错让模型改口。
func (c *webClient) search(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Query    string `json:"query"`
		Platform string `json:"platform"`
	}
	if err := decodeArguments(args, &input); err != nil {
		return "", err
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" {
		return "", errors.New("search requires a non-empty query")
	}
	if input.Platform == "" {
		input.Platform = "google"
	}
	results := []webSearchResult{}
	if input.Platform == "google" {
		credentials, err := c.services.Load(ctx, services.Serper)
		if err != nil {
			return "", err
		}
		requestContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var response struct {
			Organic []struct {
				Title   string `json:"title"`
				Link    string `json:"link"`
				Snippet string `json:"snippet"`
			} `json:"organic"`
		}
		if err := c.postJSON(requestContext, "https://google.serper.dev/search", "X-API-KEY", credentials.APIKey, map[string]string{"q": input.Query}, &response); err != nil {
			return "", err
		}
		for _, result := range response.Organic {
			link := strings.TrimSpace(result.Link)
			if link == "" {
				continue
			}
			title := strings.TrimSpace(result.Title)
			if title == "" {
				title = link
			}
			results = append(results, webSearchResult{title, link, strings.TrimSpace(result.Snippet)})
		}
	} else {
		platform, action := "wechat_oa", "article_search"
		if input.Platform == "rednote" {
			platform, action = "xiaohongshu", "search"
		} else if input.Platform != "weixin" {
			return "", errors.New("search platform must be google, weixin, or rednote")
		}
		credentials, err := c.services.Load(ctx, services.EveryInfra)
		if err != nil {
			return "", err
		}
		requestContext, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		var response struct {
			Results *[]struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Summary string `json:"summary"`
				Text    string `json:"text"`
			} `json:"results"`
		}
		if err := c.postJSON(requestContext, "https://api.everyinfra.com/api/v1/social", "Authorization", "Bearer "+credentials.APIKey,
			map[string]any{"platform": platform, "action": action, "params": map[string]string{"keyword": input.Query}}, &response); err != nil {
			return "", err
		}
		if response.Results == nil {
			return "", errors.New("EveryInfra returned an invalid search response")
		}
		for _, result := range *response.Results {
			title, link := strings.TrimSpace(result.Title), strings.TrimSpace(result.URL)
			if title == "" || link == "" {
				continue
			}
			description := result.Summary
			if input.Platform == "rednote" {
				description = result.Text
			}
			results = append(results, webSearchResult{title, link, strings.TrimSpace(description)})
		}
	}
	encoded, err := json.Marshal(struct {
		Results []webSearchResult `json:"results"`
	}{results})
	return string(encoded), err
}
