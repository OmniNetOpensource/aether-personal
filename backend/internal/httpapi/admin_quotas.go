// 本文件实现管理看板的 API 额度查询：用已保存的 Key 向各供应商实时询问剩余额度，只返回数字与固定说明，不返回供应商原始响应。

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
)

// serviceQuota 是一个服务在看板上展示的额度：查询不到时用状态说明原因，绝不猜测或伪造数字。
type serviceQuota struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Remaining *float64 `json:"remaining"`
	Used      *float64 `json:"used"`
	Limit     *float64 `json:"limit"`
	Unit      string   `json:"unit"`
	Detail    string   `json:"detail"`
}

// quotaHTTPError 只携带供应商的 HTTP 状态码，响应正文可能含账户信息，不进入看板与日志。
type quotaHTTPError struct{ status int }

// Error 满足 error 接口，文案只含状态码。
func (e quotaHTTPError) Error() string { return fmt.Sprintf("quota query returned HTTP %d", e.status) }

// adminQuotas 并行查询全部服务，单个供应商慢或失败只影响它自己那一项。
func (s *server) adminQuotas(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	items := make([]serviceQuota, len(services.Definitions))
	var waitGroup sync.WaitGroup
	for index, definition := range services.Definitions {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			items[index] = s.fetchServiceQuota(ctx, definition)
		}()
	}
	waitGroup.Wait()
	writeAdminJSON(w, map[string]any{"items": items, "fetched_at": time.Now().UTC()})
}

// fetchServiceQuota 先从凭证库取出当前 Key，再按供应商各自的额度接口查询；没有额度接口的服务直接说明原因。
func (s *server) fetchServiceQuota(ctx context.Context, definition services.Definition) serviceQuota {
	quota := serviceQuota{ID: definition.ID, Name: definition.Name, Status: "error", Detail: "查询失败，请稍后刷新重试"}
	// Parallel 的余额接口只接受账户 OAuth 令牌，不接受看板保存的产品 API Key，调用必然被拒，直接说明。
	if definition.ID == services.Parallel {
		quota.Status = "unsupported"
		quota.Detail = "余额接口只接受 Parallel 账户令牌，不接受产品 API Key；请在 Parallel 平台查看余额"
	}
	credentials, err := s.services.Load(ctx, definition.ID)
	if errors.Is(err, services.ErrNotConfigured) {
		quota.Status, quota.Detail = "not_configured", "尚未保存 Key"
		return quota
	}
	if errors.Is(err, services.ErrUnreadable) {
		quota.Status, quota.Detail = "unreadable", "凭证无法解密，请核对部署主密钥或替换 Key"
		return quota
	}
	if err != nil {
		quota.Detail = "读取已保存的凭证失败"
		return quota
	}
	if quota.Status == "unsupported" {
		return quota
	}
	switch definition.ID {
	case services.OpenRouter:
		return s.openRouterQuota(ctx, quota, credentials.APIKey)
	case services.IkunGemini:
		return s.ikunQuota(ctx, quota, credentials)
	case services.Resend:
		return s.resendQuota(ctx, quota, credentials.APIKey)
	case services.Serper:
		return s.serperQuota(ctx, quota, credentials.APIKey)
	case services.EveryInfra:
		return s.everyInfraQuota(ctx, quota, credentials.APIKey)
	}
	return quota
}

// getQuotaJSON 是额度查询共用的供应商边界：限制响应大小，非 200 只回状态码不回正文。
func (s *server) getQuotaJSON(ctx context.Context, url string, headers map[string]string, target any) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := s.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return quotaHTTPError{response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(body) > 1<<20 {
		return fmt.Errorf("quota response is too large")
	}
	return json.Unmarshal(body, target)
}

// quotaFailure 把查询错误收敛成固定说明：HTTP 状态可以展示，供应商正文与网络细节不展示。
func quotaFailure(quota serviceQuota, err error) serviceQuota {
	var httpError quotaHTTPError
	if errors.As(err, &httpError) {
		quota.Detail = fmt.Sprintf("供应商拒绝查询（HTTP %d），Key 可能已失效或没有查询权限", httpError.status)
	} else {
		quota.Detail = "暂时无法连接供应商，请稍后刷新重试"
	}
	return quota
}

// numberPointer 让可空的额度数字保持 nil 表示未知，与明确返回的 0 区分。
func numberPointer(value float64) *float64 { return &value }

// openRouterQuota 优先读账户已购额度与累计用量；普通 Key 没有管理权限时退回读这把 Key 自己的上限。
func (s *server) openRouterQuota(ctx context.Context, quota serviceQuota, apiKey string) serviceQuota {
	headers := map[string]string{"Authorization": "Bearer " + apiKey}
	var credits struct {
		Data struct {
			TotalCredits float64 `json:"total_credits"`
			TotalUsage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := s.getQuotaJSON(ctx, "https://openrouter.ai/api/v1/credits", headers, &credits); err == nil {
		quota.Status, quota.Unit = "ok", "USD"
		quota.Remaining = numberPointer(credits.Data.TotalCredits - credits.Data.TotalUsage)
		quota.Used = numberPointer(credits.Data.TotalUsage)
		quota.Limit = numberPointer(credits.Data.TotalCredits)
		quota.Detail = "账户已购总额度减去累计用量"
		return quota
	}
	var keyInfo struct {
		Data struct {
			Usage          float64  `json:"usage"`
			Limit          *float64 `json:"limit"`
			LimitRemaining *float64 `json:"limit_remaining"`
		} `json:"data"`
	}
	if err := s.getQuotaJSON(ctx, "https://openrouter.ai/api/v1/key", headers, &keyInfo); err != nil {
		return quotaFailure(quota, err)
	}
	quota.Used = numberPointer(keyInfo.Data.Usage)
	if keyInfo.Data.LimitRemaining == nil {
		quota.Status = "unsupported"
		quota.Detail = "这把 Key 未设消费上限，账户余额需要 OpenRouter 管理密钥才能查询"
		return quota
	}
	quota.Status, quota.Unit = "ok", "USD"
	quota.Remaining, quota.Limit = keyInfo.Data.LimitRemaining, keyInfo.Data.Limit
	quota.Detail = "这把 Key 在消费上限内还能使用的金额"
	return quota
}

// ikunQuota 读取 ikun（New API）的令牌额度，并按站点公开的换算单位换成它展示的计价单位。
func (s *server) ikunQuota(ctx context.Context, quota serviceQuota, credentials services.Settings) serviceQuota {
	baseURL := strings.TrimRight(credentials.BaseURL, "/")
	var usage struct {
		Code bool `json:"code"`
		Data struct {
			TotalGranted   float64 `json:"total_granted"`
			TotalUsed      float64 `json:"total_used"`
			TotalAvailable float64 `json:"total_available"`
			UnlimitedQuota bool    `json:"unlimited_quota"`
			UserQuota      *struct {
				TotalAvailable float64 `json:"total_available"`
				TotalUsed      float64 `json:"total_used"`
			} `json:"user_quota"`
		} `json:"data"`
	}
	if err := s.getQuotaJSON(ctx, baseURL+"/api/usage/token/", map[string]string{"Authorization": "Bearer " + credentials.APIKey}, &usage); err != nil {
		return quotaFailure(quota, err)
	}
	if !usage.Code {
		quota.Detail = "供应商拒绝查询，Key 可能已失效"
		return quota
	}
	available, used, granted := usage.Data.TotalAvailable, usage.Data.TotalUsed, usage.Data.TotalGranted
	if usage.Data.UserQuota != nil {
		// 令牌允许查账户额度时，真正能花的是令牌剩余额度与账户余额中更小的那个。
		available, used = usage.Data.UserQuota.TotalAvailable, usage.Data.UserQuota.TotalUsed
		if !usage.Data.UnlimitedQuota {
			available = min(available, usage.Data.TotalAvailable)
		}
		granted = available + used
	} else if usage.Data.UnlimitedQuota {
		quota.Status = "unlimited"
		quota.Detail = "这把 Key 本身不限额，且未开放账户额度查询；账户余额请在 ikun 控制台查看"
		return quota
	}
	var siteStatus struct {
		Data struct {
			QuotaPerUnit     float64 `json:"quota_per_unit"`
			QuotaDisplayType string  `json:"quota_display_type"`
			USDExchangeRate  float64 `json:"usd_exchange_rate"`
		} `json:"data"`
	}
	if err := s.getQuotaJSON(ctx, baseURL+"/api/status", nil, &siteStatus); err != nil {
		return quotaFailure(quota, err)
	}
	if siteStatus.Data.QuotaPerUnit <= 0 {
		quota.Detail = "供应商未返回额度换算单位，无法换算余额"
		return quota
	}
	convert, unit := func(units float64) float64 { return units / siteStatus.Data.QuotaPerUnit }, "USD"
	switch siteStatus.Data.QuotaDisplayType {
	case "CNY":
		rate := siteStatus.Data.USDExchangeRate
		if rate <= 0 {
			rate = 1
		}
		convert, unit = func(units float64) float64 { return units / siteStatus.Data.QuotaPerUnit * rate }, "CNY"
	case "TOKENS":
		convert, unit = func(units float64) float64 { return units }, "Token"
	}
	quota.Status, quota.Unit = "ok", unit
	quota.Remaining = numberPointer(convert(available))
	quota.Used = numberPointer(convert(used))
	quota.Limit = numberPointer(convert(granted))
	quota.Detail = "ikun 令牌剩余额度，按站点计价单位换算"
	return quota
}

// resendQuota 读取 Resend 的邮件用量：月度剩余额度是主要数字，每日额度与重置时间放在说明里。
func (s *server) resendQuota(ctx context.Context, quota serviceQuota, apiKey string) serviceQuota {
	var usage struct {
		Emails struct {
			Daily struct {
				Used     float64  `json:"used"`
				Limit    *float64 `json:"limit"`
				ResetsAt string   `json:"resets_at"`
			} `json:"daily"`
			Monthly struct {
				Used     float64  `json:"used"`
				Limit    *float64 `json:"limit"`
				ResetsAt string   `json:"resets_at"`
			} `json:"monthly"`
		} `json:"emails"`
	}
	if err := s.getQuotaJSON(ctx, "https://api.resend.com/usage", map[string]string{"Authorization": "Bearer " + apiKey}, &usage); err != nil {
		return quotaFailure(quota, err)
	}
	monthly := usage.Emails.Monthly
	quota.Used = numberPointer(monthly.Used)
	if monthly.Limit == nil {
		quota.Status = "unlimited"
		quota.Detail = "当前套餐未设置月度邮件上限；查询用量需要全权限 Key 时可能失败"
		return quota
	}
	quota.Status, quota.Unit = "ok", "封/月"
	quota.Remaining = numberPointer(*monthly.Limit - monthly.Used)
	quota.Limit = monthly.Limit
	details := []string{}
	if daily := usage.Emails.Daily; daily.Limit != nil {
		details = append(details, fmt.Sprintf("今日还可发送 %.0f 封", *daily.Limit-daily.Used))
	}
	if len(monthly.ResetsAt) >= 10 {
		details = append(details, "月度额度于 "+monthly.ResetsAt[:10]+" 重置")
	}
	quota.Detail = strings.Join(details, " · ")
	if quota.Detail == "" {
		quota.Detail = "本月邮件剩余额度"
	}
	return quota
}

// serperQuota 读取 Serper 账户接口返回的剩余次数。
func (s *server) serperQuota(ctx context.Context, quota serviceQuota, apiKey string) serviceQuota {
	var account struct {
		Balance *float64 `json:"balance"`
		Credits *float64 `json:"credits"`
	}
	if err := s.getQuotaJSON(ctx, "https://google.serper.dev/account", map[string]string{"X-API-KEY": apiKey}, &account); err != nil {
		return quotaFailure(quota, err)
	}
	remaining := account.Balance
	if remaining == nil {
		remaining = account.Credits
	}
	if remaining == nil {
		quota.Detail = "供应商响应未包含余额，无法识别剩余额度"
		return quota
	}
	quota.Status, quota.Unit = "ok", "credits"
	quota.Remaining = remaining
	quota.Detail = "Serper 账户剩余搜索次数"
	return quota
}

// everyInfraQuota 读取 EveryInfra 钱包：付费与赠送余额合并为剩余，近 24 小时消费放在说明里。
func (s *server) everyInfraQuota(ctx context.Context, quota serviceQuota, apiKey string) serviceQuota {
	var account struct {
		Balance struct {
			PaidUSD  float64  `json:"paid_usd"`
			BonusUSD float64  `json:"bonus_usd"`
			TotalUSD *float64 `json:"total_usd"`
		} `json:"balance"`
		Usage24h struct {
			Requests *float64 `json:"requests"`
			CostUSD  *float64 `json:"cost_usd"`
			SpentUSD *float64 `json:"spent_usd"`
		} `json:"usage_24h"`
	}
	if err := s.getQuotaJSON(ctx, "https://api.everyinfra.com/api/v1/account", map[string]string{"Authorization": "Bearer " + apiKey}, &account); err != nil {
		return quotaFailure(quota, err)
	}
	if account.Balance.TotalUSD == nil {
		quota.Detail = "供应商响应未包含余额，无法识别剩余额度"
		return quota
	}
	quota.Status, quota.Unit = "ok", "USD"
	quota.Remaining = account.Balance.TotalUSD
	quota.Detail = fmt.Sprintf("付费 $%.2f · 赠送 $%.2f", account.Balance.PaidUSD, account.Balance.BonusUSD)
	spent := account.Usage24h.CostUSD
	if spent == nil {
		spent = account.Usage24h.SpentUSD
	}
	if spent != nil {
		quota.Used = spent
		quota.Detail += fmt.Sprintf(" · 近 24 小时消费 $%.2f", *spent)
	} else if account.Usage24h.Requests != nil {
		quota.Detail += fmt.Sprintf(" · 近 24 小时 %.0f 次调用", *account.Usage24h.Requests)
	}
	return quota
}
