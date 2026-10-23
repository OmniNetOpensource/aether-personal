package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type webTestTransport func(*http.Request) (*http.Response, error)

func (f webTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func webTestTools(handler func(*http.Request) (int, string)) *webTools {
	return &webTools{serpAPIKey: "test-serper", parallelAPIKey: "test-parallel", everyInfraAPIKey: "test-everyinfra",
		httpClient: &http.Client{Transport: webTestTransport(func(r *http.Request) (*http.Response, error) {
			status, body := handler(r)
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
		})}}
}
func webTestBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestGoogleSearchReturnsSourcesAndEmptyResults(t *testing.T) {
	for _, test := range []struct{ name, response, expected string }{
		{"sources", `{"organic":[{"title":" Official ","link":"https://example.com/docs","snippet":" Description "},{"title":"Missing URL"},{"link":"https://example.com/fallback"}]}`, `{"results":[{"title":"Official","url":"https://example.com/docs","description":"Description"},{"title":"https://example.com/fallback","url":"https://example.com/fallback","description":""}]}`},
		{"empty", `{"organic":[]}`, `{"results":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools := webTestTools(func(r *http.Request) (int, string) {
				if r.URL.String() != "https://google.serper.dev/search" || r.Method != "POST" || r.Header.Get("X-API-KEY") != "test-serper" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				if webTestBody(t, r)["q"] != "Flutter" {
					t.Error("query not trimmed")
				}
				return 200, test.response
			})
			got, err := tools.search(context.Background(), json.RawMessage(` {"query":" Flutter "} `))
			if err != nil || got != test.expected {
				t.Fatalf("search = %s, %v", got, err)
			}
		})
	}
}

func TestSocialSearchRoutes(t *testing.T) {
	for _, test := range []struct{ platform, provider, action, field string }{
		{"weixin", "wechat_oa", "article_search", "summary"}, {"rednote", "xiaohongshu", "search", "text"},
	} {
		t.Run(test.platform, func(t *testing.T) {
			tools := webTestTools(func(r *http.Request) (int, string) {
				if r.URL.String() != "https://api.everyinfra.com/api/v1/social" || r.Header.Get("Authorization") != "Bearer test-everyinfra" {
					t.Error("unexpected social request")
				}
				body := webTestBody(t, r)
				if body["platform"] != test.provider || body["action"] != test.action || body["params"].(map[string]any)["keyword"] != "杭州" {
					t.Errorf("unexpected body: %v", body)
				}
				return 200, `{"results":[{"title":"文章","url":"https://example.com/1","` + test.field + `":"正文摘要"},{"title":"缺链接"}]}`
			})
			got, err := tools.search(context.Background(), json.RawMessage(`{"query":"杭州","platform":"`+test.platform+`"}`))
			if err != nil || !strings.Contains(got, `"description":"正文摘要"`) || strings.Contains(got, "缺链接") {
				t.Fatalf("social search = %s, %v", got, err)
			}
		})
	}
}

func TestParallelFullContentAndExcerpts(t *testing.T) {
	for _, test := range []struct{ name, page, expected string }{
		{"full", `{"full_content":" Full page ","excerpts":["excerpt"]}`, " Full page "},
		{"missing", `{"excerpts":["one"," ","two"]}`, "one\n\ntwo"},
		{"null", `{"full_content":null,"excerpts":["one"]}`, "one"},
		{"blank", `{"full_content":"  ","excerpts":["one"]}`, "one"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools := webTestTools(func(r *http.Request) (int, string) {
				if r.URL.String() != "https://api.parallel.ai/v1/extract" || r.Header.Get("x-api-key") != "test-parallel" {
					t.Error("unexpected Parallel request")
				}
				body := webTestBody(t, r)
				if body["urls"].([]any)[0] != "https://example.com/page?key=preserve" {
					t.Errorf("page URL changed: %v", body)
				}
				settings := body["advanced_settings"].(map[string]any)
				policy := settings["fetch_policy"].(map[string]any)
				if settings["full_content"] != true || policy["disable_cache_fallback"] != true || policy["timeout_seconds"] != float64(60) {
					t.Errorf("changed settings: %v", settings)
				}
				return 200, `{"results":[` + test.page + `],"errors":[]}`
			})
			got, err := tools.fetchURL(context.Background(), json.RawMessage(`{"url":"https://example.com/page?key=preserve"}`))
			if err != nil || got != test.expected {
				t.Fatalf("fetch = %q, %v", got, err)
			}
		})
	}
}

func TestSocialFetchPreservesParameters(t *testing.T) {
	for _, test := range []struct{ name, page, platform, action, response string }{
		{"wechat", "https://MP.WEIXIN.QQ.COM/s?__biz=abc&mid=123&sn=secret#fragment", "wechat_oa", "article_detail", `{"results":{"title":"文章","content_text":"正文","content_html":"<b>正文</b>"}}`},
		{"rednote", "https://rednote.com/explore/abc123?xsec_token=token&xsec_source=pc#fragment", "xiaohongshu", "note", `{"results":{"title":"笔记","text":"正文","content_html":"<b>正文</b>"}}`},
		{"images", "https://www.xiaohongshu.com/discovery/item/abc123?xsec_token=token", "xiaohongshu", "note", `{"results":{"text":"","images":["https://example.com/image.jpg"]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools := webTestTools(func(r *http.Request) (int, string) {
				body := webTestBody(t, r)
				if body["platform"] != test.platform || body["action"] != test.action {
					t.Errorf("unexpected route: %v", body)
				}
				page := body["params"].(map[string]any)["url"].(string)
				if strings.Contains(page, "#") || (test.platform == "wechat_oa" && !strings.Contains(page, "sn=secret")) || (test.platform == "xiaohongshu" && !strings.Contains(page, "xsec_token=token")) {
					t.Errorf("lost parameters: %s", page)
				}
				return 200, test.response
			})
			args, _ := json.Marshal(map[string]string{"url": test.page})
			got, err := tools.fetchURL(context.Background(), args)
			if err != nil || strings.Contains(got, "content_html") || got == "" {
				t.Fatalf("social fetch = %s, %v", got, err)
			}
		})
	}
}

func TestShortLinksStayOnPlatform(t *testing.T) {
	for _, test := range []struct {
		name, input, target, expected string
		allowed                       bool
	}{
		{"rednote", "https://xhslink.com/a/share", "https://www.xiaohongshu.com/explore/abc123?xsec_token=keep", "xiaohongshu", true},
		{"reddit", "https://www.reddit.com/r/Flutter/s/share", "https://www.reddit.com/r/Flutter/comments/AbC12/title", "https://www.reddit.com/comments/abc12/", true},
		{"reddit-escape", "https://www.reddit.com/r/Flutter/s/share", "http://127.0.0.1/private", "outside Reddit", false},
		{"rednote-escape", "https://xhslink.com/a/share", "http://127.0.0.1/private", "outside RedNote", false},
		{"rednote-port", "https://xhslink.com/a/share", "https://www.xiaohongshu.com:8443/explore/abc123", "unsupported URL", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			tools := webTestTools(func(r *http.Request) (int, string) {
				calls++
				body := webTestBody(t, r)
				if test.expected == "xiaohongshu" {
					if body["params"].(map[string]any)["url"] != test.target {
						t.Errorf("lost note URL: %v", body)
					}
					return 200, `{"results":{"text":"note"}}`
				}
				if body["urls"].([]any)[0] != test.expected {
					t.Errorf("Reddit URL = %v", body)
				}
				return 200, `{"results":[{"full_content":"post"}],"errors":[]}`
			})
			provider := tools.httpClient.Transport
			tools.httpClient.Transport = webTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == "GET" {
					calls++
					if r.URL.String() != test.input {
						t.Fatalf("followed unexpected URL %s", r.URL)
					}
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {test.target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				return provider.RoundTrip(r)
			})
			args, _ := json.Marshal(map[string]string{"url": test.input})
			got, err := tools.fetchURL(context.Background(), args)
			if test.allowed {
				if err != nil || got == "" || calls != 2 {
					t.Fatalf("fetch = %s, %v; calls=%d", got, err, calls)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.expected) || calls != 1 {
				t.Fatalf("escape = %v; calls=%d", err, calls)
			}
		})
	}
}

func TestInvalidInputDoesNotReachNetwork(t *testing.T) {
	tools := webTestTools(func(*http.Request) (int, string) { t.Fatal("invalid input reached network"); return 0, "" })
	for _, args := range []string{`null`, `[]`, `{}`, `{"query":" "}`, `{"query":"x","platform":"unknown"}`, `{"query":"x","unknown":true}`, `{"query":"x"} {}`} {
		if _, err := tools.search(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("accepted search %s", args)
		}
	}
	for _, args := range []string{`{}`, `{"url":"file:///etc/passwd"}`, `{"url":"https://user:password@example.com"}`, `{"url":"/relative"}`, `{"url":"https://example.com","unknown":true}`} {
		if _, err := tools.fetchURL(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("accepted fetch %s", args)
		}
	}
}

func TestProviderErrorsAreNotSuccessOrRetried(t *testing.T) {
	for _, test := range []struct {
		name, response, expected string
		status                   int
		search                   bool
	}{
		{"rate-limit", `{"error":{"message":"rate limited"}}`, "HTTP 429", 429, true},
		{"logical-error", `{"error":{"code":"upstream_failed","message":"unavailable","request_id":"request123"}}`, "upstream_failed unavailable request123", 200, true},
		{"invalid-json", `<html>error</html>`, "invalid JSON", 200, true},
		{"extract-error", `{"results":[],"errors":[{"error_type":"fetch_error","http_status_code":403,"content":"forbidden"}]}`, "fetch_error HTTP 403 forbidden", 200, false},
		{"no-content", `{"results":[{"full_content":" ","excerpts":[" "]}],"errors":[]}`, "no page content", 200, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			tools := webTestTools(func(*http.Request) (int, string) { calls++; return test.status, test.response })
			var err error
			if test.search {
				_, err = tools.search(context.Background(), json.RawMessage(`{"query":"test"}`))
			} else {
				_, err = tools.fetchURL(context.Background(), json.RawMessage(`{"url":"https://example.com"}`))
			}
			if err == nil || !strings.Contains(err.Error(), test.expected) || calls != 1 {
				t.Fatalf("error=%v; calls=%d", err, calls)
			}
		})
	}
}

func TestCancellationStopsWebRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tools := &webTools{parallelAPIKey: "test", httpClient: &http.Client{Transport: webTestTransport(func(r *http.Request) (*http.Response, error) {
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}}
	_, err := tools.fetchURL(ctx, json.RawMessage(`{"url":"https://example.com"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestResponseSizeIsBounded(t *testing.T) {
	tools := webTestTools(func(*http.Request) (int, string) { return 200, strings.Repeat(" ", maxWebResponseBytes+1) })
	_, err := tools.search(context.Background(), json.RawMessage(`{"query":"test"}`))
	if err == nil || !strings.Contains(err.Error(), "exceeds 8 MiB") {
		t.Fatalf("size error=%v", err)
	}
}

func TestMissingKeysDoNotReachNetwork(t *testing.T) {
	tools := webTestTools(func(*http.Request) (int, string) { t.Fatal("missing key reached network"); return 0, "" })
	tools.serpAPIKey, tools.parallelAPIKey, tools.everyInfraAPIKey = "", "", ""
	for _, args := range []string{`{"query":"test"}`, `{"query":"test","platform":"weixin"}`, `{"query":"test","platform":"rednote"}`} {
		if _, err := tools.search(context.Background(), json.RawMessage(args)); err == nil || !strings.Contains(err.Error(), "API_KEY is not set") {
			t.Errorf("missing search key=%v", err)
		}
	}
	for _, args := range []string{`{"url":"https://example.com"}`, `{"url":"https://mp.weixin.qq.com/s/article"}`} {
		if _, err := tools.fetchURL(context.Background(), json.RawMessage(args)); err == nil || !strings.Contains(err.Error(), "API_KEY is not set") {
			t.Errorf("missing fetch key=%v", err)
		}
	}
}

func TestRateWaitRespectsCancellation(t *testing.T) {
	tools := webTestTools(func(*http.Request) (int, string) { t.Fatal("canceled wait reached network"); return 0, "" })
	tools.nextSearch = time.Now().Add(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := tools.search(ctx, json.RawMessage(`{"query":"test"}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rate wait error=%v", err)
	}
}

func TestShareRedirectsStopAfterThreeRequests(t *testing.T) {
	for _, page := range []string{"https://xhslink.com/a/share", "https://www.reddit.com/r/Flutter/s/share"} {
		t.Run(page, func(t *testing.T) {
			calls := 0
			tools := &webTools{everyInfraAPIKey: "test", httpClient: &http.Client{Transport: webTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": {page}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})}}
			args, _ := json.Marshal(map[string]string{"url": page})
			_, err := tools.fetchURL(context.Background(), args)
			if err == nil || !strings.Contains(err.Error(), "exceeded 3 redirects") || calls != 3 {
				t.Fatalf("redirect error=%v; calls=%d", err, calls)
			}
		})
	}
}

func TestProviderRedirectDoesNotForwardCredential(t *testing.T) {
	calls := 0
	tools := &webTools{serpAPIKey: "test-secret", httpClient: &http.Client{Transport: webTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Hostname() != "google.serper.dev" {
			t.Fatal("provider credential followed redirect")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://example.com/steal"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}}
	_, err := tools.search(context.Background(), json.RawMessage(`{"query":"test"}`))
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") || calls != 1 {
		t.Fatalf("redirect error=%v; calls=%d", err, calls)
	}
}
