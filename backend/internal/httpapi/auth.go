// 本文件负责用户认证 HTTP 接口与会话校验：提供登录配置、验证码发信与核销、开发登录、会话查询、退出登录及 Bearer 中间件。

package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"net/netip"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const devUserEmail = "dev@example.com"

// authConfigResponse 告诉未登录页当前环境是否开放开发登录，以及生产 Turnstile 公开站点密钥。
type authConfigResponse struct {
	DevLogin         bool   `json:"dev_login"`
	TurnstileSiteKey string `json:"turnstile_site_key,omitempty"`
}

// authTokenResponse 在验证码登录或开发登录成功后返回新的会话令牌与归一化邮箱。
type authTokenResponse struct {
	Token string `json:"token"`
	Email string `json:"email"`
}

// authSessionResponse 返回当前登录账号的邮箱与个人电脑工作区可用状态。
type authSessionResponse struct {
	Email            string `json:"email"`
	DesktopAvailable bool   `json:"desktop_available"`
}

// requireChatSession 用 Bearer 令牌校验用户会话，并把完整会话行传给需要 session.ID 或邮箱的接口。
func (s *server) requireChatSession(next func(http.ResponseWriter, *http.Request, database.GetActiveSessionRow)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || token == "" {
			http.Error(w, "请先登录", http.StatusUnauthorized)
			return
		}
		session, err := s.findSession(ctx, token)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "登录已过期，请重新登录", http.StatusUnauthorized)
			return
		}
		if err != nil {
			internalError(w, "read chat session", err)
			return
		}
		next(w, r.WithContext(ctx), session)
	}
}

// requireChatUser 用现有 Bearer 会话确定操作对象，账号 ID 由服务端提供。
func (s *server) requireChatUser(next func(http.ResponseWriter, *http.Request, uuid.UUID)) http.HandlerFunc {
	return s.requireChatSession(func(w http.ResponseWriter, r *http.Request, session database.GetActiveSessionRow) {
		next(w, r, session.UserID)
	})
}

// getAuthConfig 向未登录页下发登录方式配置，避免在登录前建立聊天 WebSocket。
func (s *server) getAuthConfig(w http.ResponseWriter, r *http.Request) {
	devLogin := s.appConfig.AppEnv == "dev"
	response := authConfigResponse{DevLogin: devLogin}
	if !devLogin {
		response.TurnstileSiteKey = s.appConfig.TurnstileSiteKey
	}
	writeAdminJSON(w, response)
}

// requestAuthCode 校验 Turnstile 与共享频率窗口后发送 6 位登录验证码。
func (s *server) requestAuthCode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var body struct {
		Email          string `json:"email"`
		TurnstileToken string `json:"turnstile_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		http.Error(w, "请求格式无效", http.StatusBadRequest)
		return
	}
	// 生产源站已由 Caddy 验证 Worker 凭证，专用 IP 头只由 Worker 覆盖写入。
	clientIP, _ := netip.ParseAddr(r.Header.Get("X-Aether-Client-IP"))
	if status, message := s.requestLoginCodeByEmail(ctx, body.Email, body.TurnstileToken, clientIP); status != 0 {
		http.Error(w, message, status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verifyAuthCode 核销邮箱验证码并返回新签发的会话令牌。
func (s *server) verifyAuthCode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	var body struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		http.Error(w, "请求格式无效", http.StatusBadRequest)
		return
	}
	email, token, status, message := s.verifyLoginCodeByEmail(ctx, body.Email, body.Code)
	if status != 0 {
		http.Error(w, message, status)
		return
	}
	writeAdminJSON(w, authTokenResponse{Token: token, Email: email})
}

// devLogin 仅在 dev 环境下为固定测试账号签发普通会话，prod 直接拒绝。
func (s *server) devLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.appConfig.AppEnv != "dev" {
		http.Error(w, "当前环境不开放开发测试登录", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	token, err := s.createUserSession(ctx, s.queries, devUserEmail)
	if err != nil {
		internalError(w, "create dev session", err)
		return
	}
	writeAdminJSON(w, authTokenResponse{Token: token, Email: devUserEmail})
}

// getAuthSession 返回当前有效会话对应的账号信息与只读桌面可用性。
func (s *server) getAuthSession(w http.ResponseWriter, r *http.Request, session database.GetActiveSessionRow) {
	desktopAvailable, err := s.sandbox.Available(r.Context(), session.UserID)
	if err != nil {
		internalError(w, "check desktop availability", err)
		return
	}
	writeAdminJSON(w, authSessionResponse{
		Email:            session.Email,
		DesktopAvailable: desktopAvailable,
	})
}

// logoutAuthSession 删除当前设备的会话记录，不影响同账号其他已登录设备。
func (s *server) logoutAuthSession(w http.ResponseWriter, r *http.Request, session database.GetActiveSessionRow) {
	if err := s.queries.DeleteSession(r.Context(), session.ID); err != nil {
		internalError(w, "delete session", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verifyLoginCodeByEmail 核对验证码并签发会话；成功返回归一化邮箱与令牌，失败返回 HTTP 状态码与面向用户的中文提示。
func (s *server) verifyLoginCodeByEmail(ctx context.Context, rawEmail string, code string) (string, string, int, string) {
	email, valid := parseEmail(rawEmail)
	if !valid {
		return "", "", http.StatusBadRequest, "请输入有效的邮箱地址"
	}

	var token string
	failureStatus := http.StatusBadRequest
	failureMessage := "验证码不正确或已过期"
	// 行锁持续到会话创建与验证码删除一起提交，保证并发正确提交也只能成功一次。
	err := pgx.BeginFunc(ctx, s.databasePool, func(tx pgx.Tx) error {
		queries := database.New(tx)
		savedCodeHash, err := queries.UseLoginCodeAttempt(ctx, email)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		inputCodeHash := sha256.Sum256([]byte(code))
		if subtle.ConstantTimeCompare(savedCodeHash, inputCodeHash[:]) != 1 {
			// 错误验证码也提交尝试次数，不能通过回滚无限猜测。
			return nil
		}
		token, err = s.createUserSession(ctx, queries, email)
		if err != nil {
			return err
		}
		if err := queries.DeleteLoginCode(ctx, email); err != nil {
			return err
		}
		failureStatus = 0
		failureMessage = ""
		return nil
	})
	if err != nil {
		slog.Error("verify login code transaction failed", "error", err)
		return "", "", http.StatusInternalServerError, "服务暂时不可用，请稍后重试"
	}
	return email, token, failureStatus, failureMessage
}

// generateOpaqueToken 生成 32 字节密码学随机令牌及其 SHA-256 哈希，数据库只保存哈希值。
func generateOpaqueToken() (string, [32]byte) {
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	return token, sha256.Sum256([]byte(token))
}

// createUserSession 使用调用方的查询事务，验证码登录与开发登录共用签发逻辑。
func (s *server) createUserSession(ctx context.Context, queries *database.Queries, email string) (string, error) {
	userID, err := queries.UpsertUserByEmail(ctx, email)
	if err != nil {
		return "", err
	}
	token, tokenHash := generateOpaqueToken()
	if err := queries.CreateSession(ctx, database.CreateSessionParams{
		UserID:    userID,
		TokenHash: tokenHash[:],
	}); err != nil {
		return "", err
	}
	return token, nil
}

func (s *server) findSession(ctx context.Context, token string) (database.GetActiveSessionRow, error) {
	tokenHash := sha256.Sum256([]byte(token))
	session, err := s.queries.GetActiveSession(ctx, tokenHash[:])
	if err != nil {
		return session, err
	}
	return session, s.queries.ExtendSession(ctx, session.ID)
}

func parseEmail(input string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(input))
	address, err := mail.ParseAddress(email)
	return email, err == nil && address.Address == email
}

func internalError(w http.ResponseWriter, message string, err error) {
	slog.Error(message, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
