// 本文件读取 ikun Claude 可用模型目录，提供 Main 与压缩模型及其输入、输出容量，不把官方价格当作渠道价格。

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
)

// ikunClaudeModels 提供 Claude 模型候选；目录缺少能力字段时，只有已核对官方容量的 Opus 型号可以使用。
func (s *server) ikunClaudeModels(ctx context.Context, credentials services.Settings) ([]adminModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	models := []adminModel{}
	seen := map[string]bool{}
	after := ""
	for {
		endpoint := credentials.BaseURL + "/v1/models?limit=1000"
		if after != "" {
			endpoint += "&after_id=" + url.QueryEscape(after)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("x-api-key", credentials.APIKey)
		request.Header.Set("anthropic-version", "2023-06-01")
		request.Header.Set("User-Agent", "aether")
		client := *s.httpClient
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if response.StatusCode != http.StatusOK || len(body) > 16<<20 {
			return nil, fmt.Errorf("Claude model catalog unavailable (HTTP %d)", response.StatusCode)
		}
		var page struct {
			Data []struct {
				ID           string `json:"id"`
				Name         string `json:"display_name"`
				InputTokens  int64  `json:"max_input_tokens"`
				OutputTokens int32  `json:"max_tokens"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode Claude model catalog: %w", err)
		}
		for _, entry := range page.Data {
			if !strings.HasPrefix(entry.ID, "claude-") || len(entry.ID) > 155 || strings.ContainsAny(entry.ID, "/?#:") || seen[entry.ID] {
				continue
			}
			seen[entry.ID] = true
			// ikun 的基础目录可能只列 ID；这些型号的 1M/128K 能力来自 Anthropic 官方模型说明。
			// https://platform.claude.com/docs/en/build-with-claude/context-windows
			switch entry.ID {
			case "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-opus-5-5":
				if entry.InputTokens == 0 {
					entry.InputTokens = 1000000
				}
				if entry.OutputTokens == 0 {
					entry.OutputTokens = 128000
				}
			}
			if entry.InputTokens <= 0 || entry.OutputTokens <= 0 {
				continue
			}
			if entry.Name == "" {
				entry.Name = entry.ID
			}
			limit := entry.OutputTokens
			models = append(models, adminModel{ID: "ikun:" + entry.ID, Name: entry.Name + " · ikun", Provider: "ikun Claude", ContextLength: entry.InputTokens, MaxCompletionTokens: &limit})
		}
		if !page.HasMore {
			break
		}
		if page.LastID == "" || page.LastID == after {
			return nil, fmt.Errorf("Claude model catalog did not advance its cursor")
		}
		after = page.LastID
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("Claude model catalog has no models with known input and output limits")
	}
	return models, nil
}
