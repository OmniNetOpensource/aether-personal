// 本文件负责用户会话认证：处理邮箱验证码登录的核心校验、创建与查找会话。

package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

const devUserEmail = "dev@example.com"

// verifyLoginCodeByEmail 核对验证码并签发会话；成功返回令牌，失败返回 request_failed 事件的 code。
func (s *server) verifyLoginCodeByEmail(ctx context.Context, rawEmail string, code string) (string, string) {
	email, valid := parseEmail(rawEmail)
	if !valid {
		return "", requestFailedInvalidEmail
	}

	var token string
	failure := requestFailedInvalidCode
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
		failure = ""
		return nil
	})
	if err != nil {
		slog.Error("verify login code transaction failed", "error", err)
		return "", requestFailedInternal
	}
	return token, failure
}

// createUserSession 使用调用方的查询事务，验证码登录与开发登录共用签发逻辑。
func (s *server) createUserSession(ctx context.Context, queries *database.Queries, email string) (string, error) {
	userID, err := queries.UpsertUserByEmail(ctx, email)
	if err != nil {
		return "", err
	}
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(token))
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
