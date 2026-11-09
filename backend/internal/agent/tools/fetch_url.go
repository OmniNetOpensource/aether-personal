// 本文件实现 fetch_url 工具：校验目标网址后按平台分发抓取，微信、小红书与 Reddit 走专用通道，其余网页走 Parallel 提取正文。

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
	"github.com/google/uuid"
)

// fetchURLTool 登记 fetch_url：联网抓取在事务外执行，回执单独提交。
var fetchURLTool = Tool{
	Name:               "fetch_url",
	Definition:         json.RawMessage(`{"type":"function","function":{"name":"fetch_url","description":"Read text content from an HTTP(S) webpage, including WeChat articles, Xiaohongshu/RedNote notes and Reddit posts. Use read_image for a direct image URL. Keep the source URL when reporting findings.","parameters":{"type":"object","additionalProperties":false,"properties":{"url":{"type":"string"}},"required":["url"]}}}`),
	OutsideTransaction: true,
	Execute: func(ctx context.Context, deps Dependencies, _ *database.Queries, _ uuid.UUID, arguments string) (string, error) {
		client := newWebClient(deps)
		return client.fetchURL(ctx, json.RawMessage(arguments))
	},
}

// fetchURL 校验网址并按平台分发：特殊平台链接走专用通道，普通网页走 Parallel；分享短链只在自家域名内逐跳解析。
func (c *webClient) fetchURL(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		URL string `json:"url"`
	}
	if err := decodeArguments(args, &input); err != nil {
		return "", err
	}
	page, err := url.Parse(input.URL)
	if err != nil || page.Hostname() == "" || (page.Scheme != "http" && page.Scheme != "https") || page.User != nil {
		return "", errors.New("fetch_url requires an HTTP or HTTPS URL without credentials")
	}
	page.Host = strings.ToLower(page.Host)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if safePlatformURL(page) && page.Hostname() == "mp.weixin.qq.com" && (page.Path == "/s" || strings.HasPrefix(page.Path, "/s/")) {
		page.Fragment = ""
		return c.fetchEveryInfra(ctx, page, "wechat_oa")
	}
	if note := rednoteURL(page); note != nil {
		return c.fetchEveryInfra(ctx, note, "xiaohongshu")
	}
	if safePlatformURL(page) && page.Hostname() == "xhslink.com" && page.Path != "/" && page.Path != "" {
		page.Fragment = ""
		return c.fetchEveryInfra(ctx, page, "xiaohongshu")
	}
	if id := redditPostID(page); id != "" {
		return c.fetchParallel(ctx, "https://www.reddit.com/comments/"+id+"/")
	}
	if redditShareURL(page) {
		requestContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		for range 3 {
			page, err = c.platformRedirect(requestContext, page)
			if err != nil {
				return "", err
			}
			if !safePlatformURL(page) || (page.Hostname() != "redd.it" && !redditHost(page.Hostname())) {
				return "", errors.New("Reddit share URL redirected outside Reddit")
			}
			if id := redditPostID(page); id != "" {
				return c.fetchParallel(ctx, "https://www.reddit.com/comments/"+id+"/")
			}
			if !redditShareURL(page) {
				return "", errors.New("Reddit share URL did not resolve to a post")
			}
		}
		return "", errors.New("Reddit share URL exceeded 3 redirects")
	}
	return c.fetchParallel(ctx, input.URL)
}

// fetchEveryInfra 用 EveryInfra 读取微信文章或小红书笔记：小红书短链先在本平台内解析到笔记地址，再取正文与图片。
func (c *webClient) fetchEveryInfra(ctx context.Context, page *url.URL, platform string) (string, error) {
	credentials, err := c.services.Load(ctx, services.EveryInfra)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if page.Hostname() == "xhslink.com" {
		for range 3 {
			next, err := c.platformRedirect(ctx, page)
			if err != nil {
				return "", err
			}
			if !safePlatformURL(next) {
				return "", errors.New("RedNote share URL redirected to an unsupported URL")
			}
			if note := rednoteURL(next); note != nil {
				page = note
				break
			}
			if next.Hostname() != "xhslink.com" {
				return "", errors.New("RedNote share URL redirected outside RedNote")
			}
			page = next
		}
		if rednoteURL(page) == nil {
			return "", errors.New("RedNote share URL exceeded 3 redirects")
		}
	}
	action, bodyField := "article_detail", "content_text"
	if platform == "xiaohongshu" {
		action, bodyField = "note", "text"
	}
	var response struct {
		Results map[string]json.RawMessage `json:"results"`
	}
	if err := c.postJSON(ctx, "https://api.everyinfra.com/api/v1/social", "Authorization", "Bearer "+credentials.APIKey,
		map[string]any{"platform": platform, "action": action, "params": map[string]string{"url": page.String()}}, &response); err != nil {
		return "", err
	}
	var text string
	json.Unmarshal(response.Results[bodyField], &text)
	var images []string
	json.Unmarshal(response.Results["images"], &images)
	hasImage := false
	for _, image := range images {
		hasImage = hasImage || strings.TrimSpace(image) != ""
	}
	if strings.TrimSpace(text) == "" && !(platform == "xiaohongshu" && hasImage) {
		return "", errors.New("EveryInfra returned no page content")
	}
	delete(response.Results, "content_html")
	encoded, err := json.Marshal(response.Results)
	return string(encoded), err
}

// fetchParallel 用 Parallel 提取普通网页正文：供应商明确报错时原样收成工具错误，没有正文时才用摘录兜底。
func (c *webClient) fetchParallel(ctx context.Context, pageURL string) (string, error) {
	credentials, err := c.services.Load(ctx, services.Parallel)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	var response struct {
		Results []struct {
			FullContent string   `json:"full_content"`
			Excerpts    []string `json:"excerpts"`
		} `json:"results"`
		Errors []struct {
			ErrorType      string `json:"error_type"`
			HTTPStatusCode *int   `json:"http_status_code"`
			Content        string `json:"content"`
		} `json:"errors"`
	}
	if err := c.postJSON(ctx, "https://api.parallel.ai/v1/extract", "x-api-key", credentials.APIKey,
		map[string]any{"urls": []string{pageURL}, "advanced_settings": map[string]any{"full_content": true,
			"fetch_policy": map[string]any{"max_age_seconds": 600, "timeout_seconds": 60, "disable_cache_fallback": true}}}, &response); err != nil {
		return "", err
	}
	if len(response.Errors) > 0 {
		failure := response.Errors[0]
		status := ""
		if failure.HTTPStatusCode != nil {
			status = fmt.Sprintf(" HTTP %d", *failure.HTTPStatusCode)
		}
		return "", fmt.Errorf("Parallel extraction failed: %s%s %s", failure.ErrorType, status, boundedWebError(failure.Content))
	}
	if len(response.Results) > 0 {
		page := response.Results[0]
		if strings.TrimSpace(page.FullContent) != "" {
			return page.FullContent, nil
		}
		excerpts := []string{}
		for _, excerpt := range page.Excerpts {
			if strings.TrimSpace(excerpt) != "" {
				excerpts = append(excerpts, excerpt)
			}
		}
		if len(excerpts) > 0 {
			return strings.Join(excerpts, "\n\n"), nil
		}
	}
	return "", errors.New("Parallel returned no page content")
}

// safePlatformURL 判断网址是否可交给平台专用通道：只接受无凭证、无显式端口的 HTTP(S) 地址。
func safePlatformURL(page *url.URL) bool {
	return (page.Scheme == "http" || page.Scheme == "https") && page.User == nil && page.Port() == ""
}

// rednotePath 匹配小红书笔记详情页的路径形状。
var rednotePath = regexp.MustCompile(`(?i)^/(explore|discovery/item)/[a-z0-9]+(/|$)`)

// rednoteURL 把小红书与 RedNote 域名的笔记链接归一到 xiaohongshu.com，不是笔记链接时返回 nil。
func rednoteURL(page *url.URL) *url.URL {
	if !safePlatformURL(page) || !rednotePath.MatchString(page.Path) {
		return nil
	}
	switch page.Hostname() {
	case "xiaohongshu.com", "www.xiaohongshu.com", "rednote.com", "www.rednote.com":
		note := *page
		note.Host = "www.xiaohongshu.com"
		note.Fragment = ""
		return &note
	}
	return nil
}

// redditHost 判断主机名是否为 Reddit 自家域名。
func redditHost(host string) bool {
	switch host {
	case "reddit.com", "www.reddit.com", "old.reddit.com", "new.reddit.com", "np.reddit.com":
		return true
	}
	return false
}

// redditID 匹配 Reddit 帖子 ID 的字符形状。
var redditID = regexp.MustCompile(`(?i)^[a-z0-9]+$`)

// redditPostID 从帖子链接或 redd.it 短链里提取帖子 ID，认不出时返回空字符串。
func redditPostID(page *url.URL) string {
	if !safePlatformURL(page) {
		return ""
	}
	parts := strings.FieldsFunc(page.Path, func(r rune) bool { return r == '/' })
	var id string
	if page.Hostname() == "redd.it" && len(parts) > 0 {
		id = parts[0]
	} else if redditHost(page.Hostname()) {
		for index, part := range parts {
			if part == "comments" && index+1 < len(parts) {
				id = parts[index+1]
				break
			}
		}
		if id == "" && len(parts) > 1 && parts[0] == "gallery" {
			id = parts[1]
		}
	}
	if redditID.MatchString(id) {
		return strings.ToLower(id)
	}
	return ""
}

// redditShareURL 判断链接是否为待解析的 Reddit 分享短链形状。
func redditShareURL(page *url.URL) bool {
	parts := strings.FieldsFunc(page.Path, func(r rune) bool { return r == '/' })
	return safePlatformURL(page) && redditHost(page.Hostname()) && len(parts) == 4 && parts[0] == "r" && parts[2] == "s"
}
