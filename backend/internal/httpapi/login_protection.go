// 本文件在验证码发信边界校验 Turnstile，并用 PostgreSQL 共享计数限制验证码防刷；不限制聊天或模型额度。

package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// requestLoginCodeByEmail 先限制验证尝试，再验证单次机器人令牌，最后原子占用发信名额与邮箱冷却窗口；
// 成功返回 (0, "")，失败返回对应 HTTP 状态码与中文提示。
func (s *server) requestLoginCodeByEmail(ctx context.Context, rawEmail, challengeToken string, clientIP netip.Addr) (int, string) {
	email, valid := parseEmail(rawEmail)
	if !valid || len(email) > 254 {
		return http.StatusBadRequest, "请输入有效的邮箱地址"
	}
	globalHash := sha256.Sum256(nil)
	emailHash := sha256.Sum256([]byte(email))
	if s.appConfig.AppEnv == "prod" {
		if !clientIP.IsValid() || s.appConfig.TurnstileSiteKey == "" || s.appConfig.TurnstileSecretKey == "" || s.appConfig.TurnstileHostname == "" {
			return http.StatusServiceUnavailable, "登录服务暂不可用，请稍后再试"
		}
		// 归一化 IPv4，并以 IPv6 /64 为同一来源，避免只换接口标识就绕过计数。
		clientIP = clientIP.Unmap()
		ipKey := clientIP.String()
		if clientIP.Is6() {
			ipKey = netip.PrefixFrom(clientIP, 64).Masked().String()
		}
		ipHash := sha256.Sum256([]byte(ipKey))
		// 先检查来源窗口，已被限流的来源不能继续消耗其他用户共享的验证容量。
		allowed, err := useLoginRateLimits(ctx, s.queries, []database.UseLoginRateLimitParams{
			{Scope: "challenge_ip_minute", KeyHash: ipHash[:], WindowSeconds: 60, MaxAttempts: 5},
			{Scope: "challenge_ip_hour", KeyHash: ipHash[:], WindowSeconds: 3600, MaxAttempts: 30},
			{Scope: "challenge_global", KeyHash: globalHash[:], WindowSeconds: 60, MaxAttempts: 100},
		})
		if err != nil {
			slog.Error("reserve login challenge attempt failed", "error", err)
			return http.StatusServiceUnavailable, "登录服务暂不可用，请稍后再试"
		}
		if !allowed {
			return http.StatusTooManyRequests, "请求过于频繁，请稍后再试"
		}
		// 清理也受全局尝试频率限制；无效挑战流量不能让旧计数永久残留。
		if err := s.queries.DeleteExpiredLoginRateLimits(ctx); err != nil {
			slog.Error("clean login rate limits failed", "error", err)
			return http.StatusServiceUnavailable, "登录服务暂不可用，请稍后再试"
		}
		if challengeToken == "" || len(challengeToken) > 2048 {
			return http.StatusBadRequest, "安全验证已失效，请完成验证后重试"
		}

		// 浏览器令牌不是可信结果；只向固定的 Cloudflare 地址验证，不记录令牌或密钥。
		verificationContext, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		form := url.Values{"secret": {s.appConfig.TurnstileSecretKey}, "response": {challengeToken}, "remoteip": {clientIP.String()}}
		request, err := http.NewRequestWithContext(verificationContext, http.MethodPost, "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
		if err != nil {
			return http.StatusServiceUnavailable, "登录服务暂不可用，请稍后再试"
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := s.httpClient.Do(request)
		if err != nil {
			slog.Error("login challenge verification unavailable")
			return http.StatusServiceUnavailable, "登录服务暂不可用，请稍后再试"
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
		var verification struct {
			Success  bool   `json:"success"`
			Hostname string `json:"hostname"`
			Action   string `json:"action"`
		}
		if err != nil || response.StatusCode != http.StatusOK || len(body) > 16<<10 || json.Unmarshal(body, &verification) != nil {
			return http.StatusServiceUnavailable, "登录服务暂不可用，请稍后再试"
		}
		if !verification.Success || verification.Hostname != s.appConfig.TurnstileHostname || verification.Action != "request_code" {
			return http.StatusBadRequest, "安全验证已失效，请完成验证后重试"
		}
	}

	number, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		slog.Error("generate login code failed", "error", err)
		return http.StatusInternalServerError, "服务暂时不可用，请稍后重试"
	}
	code := fmt.Sprintf("%06d", number.Int64())
	codeHash := sha256.Sum256([]byte(code))
	// 全局计数先锁，邮箱计数后锁；任何拒绝都会回滚本次发信占用，不把网络调用放在事务里。
	err = pgx.BeginFunc(ctx, s.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		if s.appConfig.AppEnv == "prod" {
			allowed, err := useLoginRateLimits(ctx, queries, []database.UseLoginRateLimitParams{
				{Scope: "mail_global_minute", KeyHash: globalHash[:], WindowSeconds: 60, MaxAttempts: 30},
				{Scope: "mail_global_day", KeyHash: globalHash[:], WindowSeconds: 86400, MaxAttempts: 1000},
				{Scope: "mail_email_day", KeyHash: emailHash[:], WindowSeconds: 86400, MaxAttempts: 20},
			})
			if err != nil {
				return err
			}
			if !allowed {
				return pgx.ErrNoRows
			}
		}
		saved, err := queries.SaveLoginCode(ctx, database.SaveLoginCodeParams{Email: email, CodeHash: codeHash[:]})
		if err == nil && saved == 0 {
			return pgx.ErrNoRows
		}
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return http.StatusTooManyRequests, "请求过于频繁，请稍后再试"
	}
	if err != nil {
		slog.Error("reserve login email failed", "error", err)
		return http.StatusInternalServerError, "服务暂时不可用，请稍后重试"
	}
	// 发送结果未知时也不退还预算，避免断网重试造成重复邮件。
	if err := s.sendLoginCode(ctx, email, code); err != nil {
		slog.Error("send login code failed", "error", err)
		return http.StatusServiceUnavailable, "登录服务暂不可用，请稍后再试"
	}
	return 0, ""
}

// useLoginRateLimits 依序占用多个共享窗口；调用方决定拒绝时保留尝试计数还是回滚发信名额。
func useLoginRateLimits(ctx context.Context, queries *database.Queries, limits []database.UseLoginRateLimitParams) (bool, error) {
	for _, limit := range limits {
		rows, err := queries.UseLoginRateLimit(ctx, limit)
		if err != nil || rows == 0 {
			return false, err
		}
	}
	return true, nil
}
