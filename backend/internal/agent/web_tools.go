// 本文件实现联网工具 search 与 fetch_url：调用外部搜索与抓取服务，并把网页内容裁剪成模型可用的文本。

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
)

const maxWebResponseBytes = 8 << 20

type webTools struct {
	httpClient *http.Client
	services   *services.Store
}

type webSearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

func (t *webTools) search(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		Query    string `json:"query"`
		Platform string `json:"platform"`
	}
	if err := decodeWebArguments(args, &input); err != nil {
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
		credentials, err := t.services.Load(ctx, services.Serper)
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
		if err := t.postJSON(requestContext, "https://google.serper.dev/search", "X-API-KEY", credentials.APIKey, map[string]string{"q": input.Query}, &response); err != nil {
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
		credentials, err := t.services.Load(ctx, services.EveryInfra)
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
		if err := t.postJSON(requestContext, "https://api.everyinfra.com/api/v1/social", "Authorization", "Bearer "+credentials.APIKey,
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

func (t *webTools) fetchURL(ctx context.Context, args json.RawMessage) (string, error) {
	var input struct {
		URL string `json:"url"`
	}
	if err := decodeWebArguments(args, &input); err != nil {
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
		return t.fetchEveryInfra(ctx, page, "wechat_oa")
	}
	if note := rednoteURL(page); note != nil {
		return t.fetchEveryInfra(ctx, note, "xiaohongshu")
	}
	if safePlatformURL(page) && page.Hostname() == "xhslink.com" && page.Path != "/" && page.Path != "" {
		page.Fragment = ""
		return t.fetchEveryInfra(ctx, page, "xiaohongshu")
	}
	if id := redditPostID(page); id != "" {
		return t.fetchParallel(ctx, "https://www.reddit.com/comments/"+id+"/")
	}
	if redditShareURL(page) {
		requestContext, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		for range 3 {
			page, err = t.platformRedirect(requestContext, page)
			if err != nil {
				return "", err
			}
			if !safePlatformURL(page) || (page.Hostname() != "redd.it" && !redditHost(page.Hostname())) {
				return "", errors.New("Reddit share URL redirected outside Reddit")
			}
			if id := redditPostID(page); id != "" {
				return t.fetchParallel(ctx, "https://www.reddit.com/comments/"+id+"/")
			}
			if !redditShareURL(page) {
				return "", errors.New("Reddit share URL did not resolve to a post")
			}
		}
		return "", errors.New("Reddit share URL exceeded 3 redirects")
	}
	return t.fetchParallel(ctx, input.URL)
}

func (t *webTools) fetchEveryInfra(ctx context.Context, page *url.URL, platform string) (string, error) {
	credentials, err := t.services.Load(ctx, services.EveryInfra)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if page.Hostname() == "xhslink.com" {
		for range 3 {
			next, err := t.platformRedirect(ctx, page)
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
	if err := t.postJSON(ctx, "https://api.everyinfra.com/api/v1/social", "Authorization", "Bearer "+credentials.APIKey,
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

func (t *webTools) fetchParallel(ctx context.Context, pageURL string) (string, error) {
	credentials, err := t.services.Load(ctx, services.Parallel)
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
	if err := t.postJSON(ctx, "https://api.parallel.ai/v1/extract", "x-api-key", credentials.APIKey,
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

func (t *webTools) postJSON(ctx context.Context, endpoint, header, credential string, body, result any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(header, credential)
	response, err := t.noRedirectClient().Do(request)
	if err != nil {
		return fmt.Errorf("web provider request failed: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxWebResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxWebResponseBytes {
		return errors.New("web provider response exceeds 8 MiB")
	}
	var failure struct {
		Error *struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
			RefID     string `json:"ref_id"`
		} `json:"error"`
	}
	json.Unmarshal(data, &failure)
	if response.StatusCode < 200 || response.StatusCode >= 300 || failure.Error != nil {
		details := ""
		if failure.Error != nil {
			details = boundedWebError(strings.Join([]string{failure.Error.Code, failure.Error.Message, failure.Error.RequestID, failure.Error.RefID}, " "))
		}
		return fmt.Errorf("%s HTTP %d: %s", request.URL.Hostname(), response.StatusCode, details)
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("%s returned invalid JSON: %w", request.URL.Hostname(), err)
	}
	return nil
}

func (t *webTools) platformRedirect(ctx context.Context, page *url.URL) (*url.URL, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, page.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; AetherBot/1.0)")
	response, err := t.noRedirectClient().Do(request)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != 301 && response.StatusCode != 302 && response.StatusCode != 303 && response.StatusCode != 307 && response.StatusCode != 308 {
		return nil, fmt.Errorf("share URL returned HTTP %d without a redirect", response.StatusCode)
	}
	location := response.Header.Get("Location")
	if location == "" {
		return nil, errors.New("share URL redirect is missing a location")
	}
	next, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	resolved := page.ResolveReference(next)
	resolved.Host = strings.ToLower(resolved.Host)
	return resolved, nil
}

func (t *webTools) noRedirectClient() *http.Client {
	client := *t.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func decodeWebArguments(args json.RawMessage, result any) error {
	args = bytes.TrimSpace(args)
	if len(args) == 0 || args[0] != '{' {
		return errors.New("tool arguments must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("tool arguments must contain one JSON object")
	}
	return nil
}

func boundedWebError(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

func safePlatformURL(page *url.URL) bool {
	return (page.Scheme == "http" || page.Scheme == "https") && page.User == nil && page.Port() == ""
}

var rednotePath = regexp.MustCompile(`(?i)^/(explore|discovery/item)/[a-z0-9]+(/|$)`)

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

func redditHost(host string) bool {
	switch host {
	case "reddit.com", "www.reddit.com", "old.reddit.com", "new.reddit.com", "np.reddit.com":
		return true
	}
	return false
}

var redditID = regexp.MustCompile(`(?i)^[a-z0-9]+$`)

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

func redditShareURL(page *url.URL) bool {
	parts := strings.FieldsFunc(page.Path, func(r rune) bool { return r == '/' })
	return safePlatformURL(page) && redditHost(page.Hostname()) && len(parts) == 4 && parts[0] == "r" && parts[2] == "s"
}
