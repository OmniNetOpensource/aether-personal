// 本文件将共享记忆引擎挂载为无状态 Streamable HTTP MCP 服务端，并通过 OAuth 2.1 Bearer 访问令牌识别当前操作用户。

package httpapi

import (
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

// mcpUserIDKey 是包内私有上下文键，用于在进程共享的 MCP Server 中为每个并发请求隔离当前用户。
type mcpUserIDKey struct{}

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
		},
	)
	return s.requireMCPMemoryAuth(streamable)
}

// requireMCPMemoryAuth 校验 OAuth 2.1 访问令牌并将用户 ID 注入请求上下文；未通过时按 RFC 9728 下发 WWW-Authenticate 指引发现。
func (s *server) requireMCPMemoryAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.requirePublicOrigin(w) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeUnauthorized := func(message string) {
			w.Header().Set(
				"WWW-Authenticate",
				fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q`, s.protectedResourceMetadataURL(), oauthMemoryScope),
			)
			http.Error(w, message, http.StatusUnauthorized)
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
			internalError(w, "lookup oauth access token", err)
			return
		}
		if tokenRow.Scope != oauthMemoryScope || tokenRow.Resource != s.mcpMemoryResourceURL() {
			writeUnauthorized("access token scope or resource mismatch")
			return
		}
		if err := s.queries.TouchOAuthTokenLastUsed(ctx, tokenRow.ID); err != nil {
			internalError(w, "touch oauth token last used", err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, mcpUserIDKey{}, tokenRow.UserID)))
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
