package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
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

type sessionContextKey struct{}

func (s *server) requestLoginCode(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	email, valid := parseEmail(request.Email)
	if !valid {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}

	number, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		internalError(w, "generate login code failed", err)
		return
	}
	code := fmt.Sprintf("%06d", number.Int64())
	codeHash := sha256.Sum256([]byte(code))

	savedRows, err := s.queries.SaveLoginCode(r.Context(), database.SaveLoginCodeParams{
		Email:    email,
		CodeHash: codeHash[:],
	})
	if err != nil {
		internalError(w, "save login code failed", err)
		return
	}
	if savedRows == 0 {
		http.Error(w, "please wait before requesting another code", http.StatusTooManyRequests)
		return
	}

	if err := s.sendLoginCode(r.Context(), email, code); err != nil {
		internalError(w, "send login code failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) verifyLoginCode(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	email, valid := parseEmail(request.Email)
	if !valid {
		http.Error(w, "invalid email", http.StatusBadRequest)
		return
	}

	savedCodeHash, err := s.queries.UseLoginCodeAttempt(r.Context(), email)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "invalid or expired code", http.StatusBadRequest)
		return
	}
	if err != nil {
		internalError(w, "use login code attempt failed", err)
		return
	}
	inputCodeHash := sha256.Sum256([]byte(request.Code))
	if subtle.ConstantTimeCompare(savedCodeHash, inputCodeHash[:]) != 1 {
		http.Error(w, "invalid or expired code", http.StatusBadRequest)
		return
	}

	userID, err := s.queries.UpsertUserByEmail(r.Context(), email)
	if err != nil {
		internalError(w, "upsert user failed", err)
		return
	}

	tokenBytes := make([]byte, 32)
	rand.Read(tokenBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(token))
	if err := s.queries.CreateSession(r.Context(), database.CreateSessionParams{
		UserID:    userID,
		TokenHash: tokenHash[:],
	}); err != nil {
		internalError(w, "create session failed", err)
		return
	}

	if err := s.queries.DeleteLoginCode(r.Context(), email); err != nil {
		internalError(w, "delete login code failed", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": token})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	session := r.Context().Value(sessionContextKey{}).(database.GetActiveSessionRow)
	if err := s.queries.DeleteSession(r.Context(), session.ID); err != nil {
		internalError(w, "delete session failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		tokenHash := sha256.Sum256([]byte(token))
		session, err := s.queries.GetActiveSession(r.Context(), tokenHash[:])
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err != nil {
			internalError(w, "get session failed", err)
			return
		}
		if err := s.queries.ExtendSession(r.Context(), session.ID); err != nil {
			internalError(w, "extend session failed", err)
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, session)))
	})
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
