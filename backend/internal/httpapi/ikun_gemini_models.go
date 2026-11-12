// 本文件读取 ikun Gemini 原生模型目录：处理分页并为 App 提供已知输入容量。

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
	"unicode/utf8"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
)

// ikunGeminiModels 处理 Gemini 模型目录分页并保留已知输入容量。
func (s *server) ikunGeminiModels(ctx context.Context, credentials services.Settings) ([]userModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	models := []userModel{}
	seenPages := map[string]bool{}
	seenModels := map[string]bool{}
	pageToken := ""
	for {
		endpoint, _ := url.Parse(credentials.BaseURL + "/v1beta/models")
		query := endpoint.Query()
		query.Set("pageSize", "1000")
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		endpoint.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("x-goog-api-key", credentials.APIKey)
		request.Header.Set("User-Agent", "aether")
		// Gemini 的自定义密钥头只发给配置的地址，不跟随重定向。
		client := *s.httpClient
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
		response.Body.Close()
		if response.StatusCode != http.StatusOK || readErr != nil || len(body) > 16<<20 {
			return nil, fmt.Errorf("Gemini model catalog unavailable (HTTP %d)", response.StatusCode)
		}
		var page struct {
			Models []struct {
				Name            string `json:"name"`
				DisplayName     string `json:"displayName"`
				InputTokenLimit int64  `json:"inputTokenLimit"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode Gemini model catalog: %w", err)
		}
		for _, entry := range page.Models {
			name := strings.TrimPrefix(entry.Name, "models/")
			if !strings.HasPrefix(name, "gemini-") || strings.ContainsAny(name, "/?#:") || len(name) > 155 || seenModels[name] {
				continue
			}
			seenModels[name] = true
			displayName := entry.DisplayName
			if displayName == "" || utf8.RuneCountInString(displayName) > 180 {
				displayName = name
			}
			model := userModel{ID: "ikun:" + name, Name: displayName + " · ikun"}
			// 缺少容量时保持未知，运行时仍按管理员 Token 阈值压缩，不猜测模型能力。
			if entry.InputTokenLimit > 0 {
				model.ContextLength = &entry.InputTokenLimit
			}
			models = append(models, model)
		}
		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
		if seenPages[pageToken] {
			return nil, fmt.Errorf("Gemini model catalog repeated a page token")
		}
		seenPages[pageToken] = true
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("Gemini model catalog has no agent model candidates")
	}
	return models, nil
}
