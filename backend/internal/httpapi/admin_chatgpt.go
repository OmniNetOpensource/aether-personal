// 本文件实现管理看板的 ChatGPT 订阅授权接口：查询脱敏状态、发起 OAuth 授权链接、提交回环回调 URL 换票以及断开连接。

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/services"
)

// adminChatGPT 返回部署级 ChatGPT 订阅的脱敏连接状态。
func (s *server) adminChatGPT(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	info, err := s.services.ChatGPTStatus(ctx)
	if err != nil {
		internalError(w, "load chatgpt status failed", err)
		return
	}
	writeAdminJSON(w, info)
}

// startAdminChatGPT 生成一次带 PKCE 的 Sign in with ChatGPT 授权链接并暂存授权流。
func (s *server) startAdminChatGPT(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	started, err := s.services.StartChatGPTAuth(ctx)
	if err != nil {
		internalError(w, "start chatgpt auth failed", err)
		return
	}
	slog.Info("admin started chatgpt oauth flow", "admin_session_id", r.Context().Value(adminSessionContextKey{}))
	writeAdminJSON(w, started)
}

// completeAdminChatGPT 接收管理员粘贴的回环回调 URL，换取并加密保存 ChatGPT 订阅令牌。
func (s *server) completeAdminChatGPT(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CallbackURL string `json:"callback_url"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid callback payload", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	info, err := s.services.CompleteChatGPTAuth(ctx, request.CallbackURL)
	if err != nil {
		switch {
		case errors.Is(err, services.ErrInvalid):
			http.Error(w, "invalid or expired callback URL", http.StatusBadRequest)
		case errors.Is(err, services.ErrUpstream):
			slog.Error("chatgpt oauth token exchange failed", "error", err)
			http.Error(w, "failed to exchange token with OpenAI", http.StatusBadGateway)
		default:
			internalError(w, "complete chatgpt auth failed", err)
		}
		return
	}
	slog.Info("admin connected chatgpt subscription", "admin_session_id", r.Context().Value(adminSessionContextKey{}), "account_sub", info.AccountSub)
	writeAdminJSON(w, info)
}

// deleteAdminChatGPT 断开部署级 ChatGPT 订阅连接并清除已保存令牌与待完成授权流。
func (s *server) deleteAdminChatGPT(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.services.DisconnectChatGPT(ctx); err != nil {
		internalError(w, "disconnect chatgpt failed", err)
		return
	}
	slog.Info("admin disconnected chatgpt subscription", "admin_session_id", r.Context().Value(adminSessionContextKey{}))
	w.WriteHeader(http.StatusNoContent)
}
