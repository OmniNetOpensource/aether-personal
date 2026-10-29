// 本文件负责用户会话认证：处理邮箱验证码登录的核心校验、创建与查找会话。

package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/mail"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

const devUserEmail = "dev@example.com"

// requestLoginCodeByEmail 生成并发送验证码；返回空字符串表示成功，否则返回 request_failed 事件的 code。
func (s *server) requestLoginCodeByEmail(ctx context.Context, rawEmail string) string {
	email, valid := parseEmail(rawEmail)
	if !valid {
		return requestFailedInvalidEmail
	}

	number, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		slog.Error("generate login code failed", "error", err)
		return requestFailedInternal
	}
	code := fmt.Sprintf("%06d", number.Int64())
	codeHash := sha256.Sum256([]byte(code))

	savedRows, err := s.queries.SaveLoginCode(ctx, database.SaveLoginCodeParams{
		Email:    email,
		CodeHash: codeHash[:],
	})
	if err != nil {
		slog.Error("save login code failed", "error", err)
		return requestFailedInternal
	}
	if savedRows == 0 {
		return requestFailedTooFrequent
	}

	if err := s.sendLoginCode(ctx, email, code); err != nil {
		slog.Error("send login code failed", "error", err)
		return requestFailedInternal
	}
	return ""
}

// verifyLoginCodeByEmail 核对验证码并签发会话；成功返回令牌，失败返回 request_failed 事件的 code。
func (s *server) verifyLoginCodeByEmail(ctx context.Context, rawEmail string, code string) (string, string) {
	email, valid := parseEmail(rawEmail)
	if !valid {
		return "", requestFailedInvalidEmail
	}

	savedCodeHash, err := s.queries.UseLoginCodeAttempt(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", requestFailedInvalidCode
	}
	if err != nil {
		slog.Error("use login code attempt failed", "error", err)
		return "", requestFailedInternal
	}
	inputCodeHash := sha256.Sum256([]byte(code))
	if subtle.ConstantTimeCompare(savedCodeHash, inputCodeHash[:]) != 1 {
		return "", requestFailedInvalidCode
	}

	token, err := s.createUserSession(ctx, email)
	if err != nil {
		slog.Error("create login session failed", "error", err)
		return "", requestFailedInternal
	}

	if err := s.queries.DeleteLoginCode(ctx, email); err != nil {
		slog.Error("delete login code failed", "error", err)
		return "", requestFailedInternal
	}
	return token, ""
}

func (s *server) createUserSession(ctx context.Context, email string) (string, error) {
	userID, err := s.queries.UpsertUserByEmail(ctx, email)
	if err != nil {
		return "", err
	}
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(token))
	if err := s.queries.CreateSession(ctx, database.CreateSessionParams{
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
