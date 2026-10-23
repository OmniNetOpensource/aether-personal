package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// 真实供应商检查只在显式启用时运行，普通测试不会产生网络请求或费用。
func TestLiveWebTools(t *testing.T) {
	if os.Getenv("RUN_LIVE_TOOLS") != "1" {
		t.Skip("set RUN_LIVE_TOOLS=1 to call real providers")
	}
	tools := webTools{
		httpClient:       &http.Client{Timeout: 125 * time.Second},
		serpAPIKey:       os.Getenv("SERP_API_KEY"),
		parallelAPIKey:   os.Getenv("PARALLEL_API_KEY"),
		everyInfraAPIKey: os.Getenv("EVERYINFRA_API_KEY"),
	}
	for _, platform := range []string{"google", "weixin", "rednote"} {
		t.Run(platform, func(t *testing.T) {
			if platform == "google" && tools.serpAPIKey == "" {
				t.Skip("SERP_API_KEY is not configured; Google search is not verified")
			}
			if platform != "google" && tools.everyInfraAPIKey == "" {
				t.Skip("EVERYINFRA_API_KEY is not configured")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
			defer cancel()
			arguments, _ := json.Marshal(map[string]string{"query": "Flutter", "platform": platform})
			result, err := tools.search(ctx, arguments)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Results []webSearchResult `json:"results"`
			}
			if err := json.Unmarshal([]byte(result), &response); err != nil {
				t.Fatal(err)
			}
			if response.Results == nil {
				t.Fatal("search did not return a results array")
			}
			for _, row := range response.Results {
				if row.Title == "" || row.URL == "" {
					t.Fatal("search result is missing a title or source URL")
				}
			}
			t.Logf("real %s response: %d source links", platform, len(response.Results))
			if platform == "rednote" && len(response.Results) > 0 {
				arguments, _ := json.Marshal(map[string]string{"url": response.Results[0].URL})
				page, err := tools.fetchURL(ctx, arguments)
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(page) == "" {
					t.Fatal("RedNote returned no readable note")
				}
				t.Logf("real RedNote note: %d characters", len([]rune(page)))
			}
		})
	}
	t.Run("parallel", func(t *testing.T) {
		if tools.parallelAPIKey == "" {
			t.Skip("PARALLEL_API_KEY is not configured")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		text, err := tools.fetchURL(ctx, json.RawMessage(`{"url":"https://docs.flutter.dev/platform-integration/web/setup"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(text), "flutter") {
			t.Fatal("extracted page does not contain the requested documentation")
		}
		t.Logf("real Parallel response: %d characters", len([]rune(text)))
	})
}
