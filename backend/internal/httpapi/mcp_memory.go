// 本文件将共享记忆引擎挂载为无状态 Streamable HTTP MCP 服务端，并通过 OAuth 2.1 Bearer 访问令牌识别当前操作用户。

package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/memory"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpErrorLogMaxBytes = 512

// mcpUserIDKey 是包内私有上下文键，用于在进程共享的 MCP Server 中为每个并发请求隔离当前用户。
type mcpUserIDKey struct{}

// mcpResponseRecorder 包装底层 ResponseWriter，在不复制成功响应体的前提下记录失败状态码与截断后的错误正文。
type mcpResponseRecorder struct {
	http.ResponseWriter
	status    int
	errorBody bytes.Buffer
}

// WriteHeader 记录首次写出的 HTTP 状态码并透传给底层响应。
func (r *mcpResponseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

// Write 在未显式写头时按标准库语义记为 200，并仅在 4xx/5xx 响应时截取前 512 字节错误文本用于日志排查。
func (r *mcpResponseRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.status >= http.StatusBadRequest {
		if remaining := mcpErrorLogMaxBytes - r.errorBody.Len(); remaining > 0 {
			r.errorBody.Write(data[:min(len(data), remaining)])
		}
	}
	return r.ResponseWriter.Write(data)
}

// Unwrap 暴露原始 ResponseWriter，使标准库 http.ResponseController 能继续执行 Flush 与超时控制。
func (r *mcpResponseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// newMemoryMCPHandler 构造进程级共享的无状态 MCP Streamable HTTP 处理器，适配生产双副本轮询部署。
func (s *server) newMemoryMCPHandler() http.Handler {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{Name: "aether-memory", Version: "1.0.0"},
		&mcp.ServerOptions{Instructions: memory.MCPInstructions},
	)
	mcpServer.AddTool(&mcp.Tool{
		Name:        memory.ToolName,
		Description: memory.ToolDescription,
		InputSchema: memory.InputSchema,
	}, s.handleMemoryMCPTool)

	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return mcpServer },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			Logger:       slog.Default(),
		},
	)
	return s.requireMCPMemoryAuth(streamable)
}

// requireMCPMemoryAuth 校验 OAuth 2.1 访问令牌、注入用户 ID 并记录非 2xx 拒绝细节；未通过时按 RFC 9728 下发 WWW-Authenticate 指引发现。
func (s *server) requireMCPMemoryAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &mcpResponseRecorder{ResponseWriter: w}
		// 统一在请求结束时记录 4xx/5xx 的协议头与错误文本，避免 SDK 静默拒绝外部客户端后无日志可查；绝不记录令牌原文。
		defer func() {
			if recorder.status < http.StatusBadRequest {
				return
			}
			slog.Warn(
				"mcp memory request failed",
				"method", r.Method,
				"status", recorder.status,
				"has_auth", r.Header.Get("Authorization") != "",
				"content_type", r.Header.Get("Content-Type"),
				"accept", r.Header.Get("Accept"),
				"origin", r.Header.Get("Origin"),
				"mcp_protocol_version", r.Header.Get("Mcp-Protocol-Version"),
				"mcp_session_id", r.Header.Get("Mcp-Session-Id"),
				"error_body", strings.TrimSpace(recorder.errorBody.String()),
			)
		}()

		if !s.requirePublicOrigin(recorder) {
			return
		}
		recorder.Header().Set("Cache-Control", "no-store")
		writeUnauthorized := func(message string) {
			recorder.Header().Set(
				"WWW-Authenticate",
				fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q`, s.protectedResourceMetadataURL(), oauthMemoryScope),
			)
			http.Error(recorder, message, http.StatusUnauthorized)
		}

		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") || fields[1] == "" {
			writeUnauthorized("missing bearer token")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		tokenHash := sha256.Sum256([]byte(fields[1]))
		tokenRow, err := s.queries.GetActiveOAuthAccessToken(ctx, tokenHash[:])
		if errors.Is(err, pgx.ErrNoRows) {
			writeUnauthorized("invalid or expired access token")
			return
		}
		if err != nil {
			internalError(recorder, "lookup oauth access token", err)
			return
		}
		if tokenRow.Scope != oauthMemoryScope || tokenRow.Resource != s.mcpMemoryResourceURL() {
			writeUnauthorized("access token scope or resource mismatch")
			return
		}
		if err := s.queries.TouchOAuthTokenLastUsed(ctx, tokenRow.ID); err != nil {
			internalError(recorder, "touch oauth token last used", err)
			return
		}
		next.ServeHTTP(recorder, r.WithContext(context.WithValue(ctx, mcpUserIDKey{}, tokenRow.UserID)))
	})
}

// handleMemoryMCPTool 从上下文取出经 OAuth 验证的用户并执行一次记忆命令，将业务校验失败映射为模型可见的 IsError 结果。
func (s *server) handleMemoryMCPTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	userID, ok := ctx.Value(mcpUserIDKey{}).(uuid.UUID)
	if !ok {
		return nil, errors.New("unauthenticated memory request")
	}
	arguments := "{}"
	if req != nil && req.Params != nil && len(req.Params.Arguments) > 0 {
		arguments = string(req.Params.Arguments)
	}
	output, isToolErr, err := memory.Execute(ctx, s.databasePool, userID, arguments)
	if err != nil {
		slog.Error("execute mcp memory tool failed", "user_id", userID, "error", err)
		return nil, errors.New("memory service temporarily unavailable")
	}
	return &mcp.CallToolResult{
		IsError: isToolErr,
		Content: []mcp.Content{
			&mcp.TextContent{Text: output},
		},
	}, nil
}
