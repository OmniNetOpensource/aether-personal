// 本文件负责管理员认证：用独立管理员密码登录、建立管理会话，并用中间件保护全部管理接口。

package httpapi

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type adminSessionContextKey struct{}

func (s *server) adminLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if len(s.appConfig.AdminPasswordKey) == 0 {
		http.Error(w, "administrator password is not configured", http.StatusServiceUnavailable)
		return
	}
	attempts, err := s.queries.UseAdminLoginAttempt(r.Context())
	if err != nil {
		internalError(w, "record admin login attempt failed", err)
		return
	}
	if attempts == 0 {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(request.Password) == 0 || len(request.Password) > 1024 {
		http.Error(w, "invalid login request", http.StatusBadRequest)
		return
	}
	key, err := pbkdf2.Key(sha256.New, request.Password, s.appConfig.AdminPasswordSalt, 600000, 32)
	if err != nil {
		internalError(w, "derive admin password failed", err)
		return
	}
	if subtle.ConstantTimeCompare(key, s.appConfig.AdminPasswordKey) != 1 {
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}
	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(token))
	credentialHash := sha256.Sum256(s.appConfig.AdminPasswordKey)
	if err := s.queries.CreateAdminSession(r.Context(), database.CreateAdminSessionParams{
		TokenHash: tokenHash[:], CredentialHash: credentialHash[:],
	}); err != nil {
		internalError(w, "create admin session failed", err)
		return
	}
	writeAdminJSON(w, map[string]string{"token": token})
}

func (s *server) adminLogout(w http.ResponseWriter, r *http.Request) {
	id := r.Context().Value(adminSessionContextKey{}).(uuid.UUID)
	if err := s.queries.DeleteAdminSession(r.Context(), id); err != nil {
		internalError(w, "delete admin session failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || token == "" || len(token) > 128 || len(s.appConfig.AdminPasswordKey) == 0 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		tokenHash := sha256.Sum256([]byte(token))
		credentialHash := sha256.Sum256(s.appConfig.AdminPasswordKey)
		id, err := s.queries.GetAdminSession(r.Context(), database.GetAdminSessionParams{
			TokenHash: tokenHash[:], CredentialHash: credentialHash[:],
		})
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err != nil {
			internalError(w, "find admin session failed", err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), adminSessionContextKey{}, id)))
	})
}
