// 本文件实现远程记忆 MCP 的 OAuth 2.1 授权层：托管 RFC 9728/8414 发现文档、RFC 7591 动态客户端注册、PKCE 授权码签发与令牌轮换撤销，以及用户侧连接管理。

package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const (
	oauthMemoryScope      = "memory"
	oauthAccessTokenTTL   = 3600
	oauthMaxBodyBytes     = 16 << 10
	oauthMaxRedirectURIs  = 10
	oauthMaxClientNameLen = 120
)

var errOAuthResourceMismatch = errors.New("oauth token resource mismatch")

// oauthAuthorizeParams 承载前端授权页读取与提交决策时共享的一组 OAuth 2.1 授权参数。
type oauthAuthorizeParams struct {
	ResponseType        string `json:"response_type"`
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	State               string `json:"state"`
	Scope               string `json:"scope"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	Resource            string `json:"resource"`
}

// validatedAuthorizeRequest 保存经过服务端校验的客户端、回调地址、PKCE 挑战与目标资源。
type validatedAuthorizeRequest struct {
	Client        database.OauthClient
	RedirectURI   string
	RedirectHost  string
	State         string
	Scope         string
	CodeChallenge string
	Resource      string
}

// oauthConnectionSummary 描述用户在 /memories 弹窗中看到的单个外部已授权应用。
type oauthConnectionSummary struct {
	ID         uuid.UUID  `json:"id"`
	ClientName string     `json:"client_name"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// mcpMemoryResourceURL 返回远程记忆 MCP 资源的规范 URL，用作受保护资源标识与令牌绑定目标。
func (s *server) mcpMemoryResourceURL() string {
	if s.appConfig.PublicOrigin == "" {
		return ""
	}
	return s.appConfig.PublicOrigin + "/mcp/memory"
}

// protectedResourceMetadataURL 返回 RFC 9728 受保护资源元数据地址，随 401 WWW-Authenticate 头下发。
func (s *server) protectedResourceMetadataURL() string {
	if s.appConfig.PublicOrigin == "" {
		return ""
	}
	return s.appConfig.PublicOrigin + "/.well-known/oauth-protected-resource/mcp/memory"
}

// requirePublicOrigin 在生产未配置 PUBLIC_ORIGIN 时明确返回 503，不猜测源站代理域名。
func (s *server) requirePublicOrigin(w http.ResponseWriter) bool {
	if s.appConfig.PublicOrigin != "" {
		return true
	}
	http.Error(w, "MCP OAuth is not configured: PUBLIC_ORIGIN is empty", http.StatusServiceUnavailable)
	return false
}

// oauthProtectedResourceMetadata 托管 RFC 9728 受保护资源发现文档，支持根路径与 /mcp/memory 子路径。
func (s *server) oauthProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicOrigin(w) {
		return
	}
	handler := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               s.mcpMemoryResourceURL(),
		AuthorizationServers:   []string{s.appConfig.PublicOrigin},
		ScopesSupported:        []string{oauthMemoryScope},
		BearerMethodsSupported: []string{"header"},
	})
	handler.ServeHTTP(w, r)
}

// oauthAuthorizationServerMetadata 托管 RFC 8414 授权服务器发现文档，声明 PKCE S256、DCR 与 RFC 9207 iss 支持。
func (s *server) oauthAuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicOrigin(w) {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, MCP-Protocol-Version")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	origin := s.appConfig.PublicOrigin
	writeOAuthJSON(w, http.StatusOK, map[string]any{
		"issuer":                                     origin,
		"authorization_endpoint":                     origin + "/oauth/authorize",
		"token_endpoint":                             origin + "/oauth/token",
		"registration_endpoint":                      origin + "/oauth/register",
		"revocation_endpoint":                        origin + "/oauth/revoke",
		"response_types_supported":                   []string{"code"},
		"grant_types_supported":                      []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":           []string{"S256"},
		"token_endpoint_auth_methods_supported":      []string{"none"},
		"scopes_supported":                           []string{oauthMemoryScope},
		"authorization_response_iss_parameter_supported": true,
	})
}

// oauthRegisterClient 实现 RFC 7591 动态客户端注册，供 ChatGPT、Gemini 与本地 MCP 客户端自动登记回调地址。
func (s *server) oauthRegisterClient(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicOrigin(w) {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if s.appConfig.AppEnv == "prod" {
		clientIP, _ := netip.ParseAddr(r.Header.Get("X-Aether-Client-IP"))
		if !clientIP.IsValid() {
			writeOAuthError(w, http.StatusForbidden, "invalid_request", "missing trusted client address")
			return
		}
		clientIP = clientIP.Unmap()
		ipKey := clientIP.String()
		if clientIP.Is6() {
			ipKey = netip.PrefixFrom(clientIP, 64).Masked().String()
		}
		ipHash := sha256.Sum256([]byte(ipKey))
		globalHash := sha256.Sum256(nil)
		allowed, err := useLoginRateLimits(r.Context(), s.queries, []database.UseLoginRateLimitParams{
			{Scope: "oauth_register_ip_hour", KeyHash: ipHash[:], WindowSeconds: 3600, MaxAttempts: 20},
			{Scope: "oauth_register_global_hour", KeyHash: globalHash[:], WindowSeconds: 3600, MaxAttempts: 200},
		})
		if err != nil {
			slog.Error("check oauth registration rate limit failed", "error", err)
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "registration unavailable")
			return
		}
		if !allowed {
			writeOAuthError(w, http.StatusTooManyRequests, "invalid_client_metadata", "too many client registrations")
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, oauthMaxBodyBytes)
	var request struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "request body must be valid JSON")
		return
	}
	if len(request.RedirectURIs) == 0 || len(request.RedirectURIs) > oauthMaxRedirectURIs {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must contain between 1 and 10 URIs")
		return
	}
	redirectURIs := make([]string, 0, len(request.RedirectURIs))
	for _, rawURI := range request.RedirectURIs {
		if !validateOAuthRedirectURI(rawURI) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must use https or loopback http without fragments")
			return
		}
		if !slices.Contains(redirectURIs, rawURI) {
			redirectURIs = append(redirectURIs, rawURI)
		}
	}
	if len(request.ResponseTypes) > 0 && !slices.Contains(request.ResponseTypes, "code") {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "response_types must include code")
		return
	}
	if len(request.GrantTypes) > 0 && !slices.Contains(request.GrantTypes, "authorization_code") {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "grant_types must include authorization_code")
		return
	}
	if request.TokenEndpointAuthMethod != "" && request.TokenEndpointAuthMethod != "none" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "only public PKCE clients (token_endpoint_auth_method=none) are supported")
		return
	}

	clientName := normalizeOAuthClientName(request.ClientName, redirectURIs[0])
	client, err := s.queries.CreateOAuthClient(r.Context(), database.CreateOAuthClientParams{
		ClientName:   clientName,
		RedirectUris: redirectURIs,
	})
	if err != nil {
		slog.Error("create oauth client failed", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to register client")
		return
	}
	writeOAuthJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  client.ID.String(),
		"client_name":                client.ClientName,
		"redirect_uris":              client.RedirectUris,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"client_id_issued_at":        client.CreatedAt.Unix(),
	})
}

// oauthAuthorizeContext 在前端授权页展示确认卡片前校验全部 OAuth 参数，并返回客户端名称与回调域名。
func (s *server) oauthAuthorizeContext(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicOrigin(w) {
		return
	}
	query := r.URL.Query()
	params := oauthAuthorizeParams{
		ResponseType:        query.Get("response_type"),
		ClientID:            query.Get("client_id"),
		RedirectURI:         query.Get("redirect_uri"),
		State:               query.Get("state"),
		Scope:               query.Get("scope"),
		CodeChallenge:       query.Get("code_challenge"),
		CodeChallengeMethod: query.Get("code_challenge_method"),
		Resource:            query.Get("resource"),
	}
	validated, redirectErrURL, pageErr, err := s.validateAuthorizeParams(r.Context(), params)
	if err != nil {
		internalError(w, "validate oauth authorize context", err)
		return
	}
	if pageErr != "" {
		writeOAuthJSON(w, http.StatusBadRequest, map[string]string{"error": pageErr})
		return
	}
	if redirectErrURL != "" {
		writeOAuthJSON(w, http.StatusOK, map[string]string{"redirect_to": redirectErrURL})
		return
	}
	writeOAuthJSON(w, http.StatusOK, map[string]string{
		"client_name":   validated.Client.ClientName,
		"redirect_host": validated.RedirectHost,
		"scope":         validated.Scope,
		"resource":      validated.Resource,
	})
}

// oauthAuthorizeDecision 校验登录会话并再次核对 OAuth 参数，同意时签发 5 分钟一次性授权码并返回带 iss 的回调地址。
func (s *server) oauthAuthorizeDecision(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	if !s.requirePublicOrigin(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, oauthMaxBodyBytes)
	var request struct {
		Approve bool `json:"approve"`
		oauthAuthorizeParams
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "请求格式无效", http.StatusBadRequest)
		return
	}
	validated, redirectErrURL, pageErr, err := s.validateAuthorizeParams(r.Context(), request.oauthAuthorizeParams)
	if err != nil {
		internalError(w, "validate oauth authorize decision", err)
		return
	}
	if pageErr != "" {
		http.Error(w, pageErr, http.StatusBadRequest)
		return
	}
	if redirectErrURL != "" {
		writeOAuthJSON(w, http.StatusOK, map[string]string{"redirect_to": redirectErrURL})
		return
	}
	if !request.Approve {
		redirectTo := buildOAuthRedirectURL(validated.RedirectURI, url.Values{
			"error":             {"access_denied"},
			"error_description": {"user denied authorization"},
			"iss":               {s.appConfig.PublicOrigin},
		}, validated.State)
		writeOAuthJSON(w, http.StatusOK, map[string]string{"redirect_to": redirectTo})
		return
	}

	if err := s.queries.DeleteExpiredOAuthAuthorizationCodes(r.Context()); err != nil {
		internalError(w, "delete expired oauth authorization codes", err)
		return
	}
	rawCode, codeHash := generateOpaqueToken()
	if err := s.queries.CreateOAuthAuthorizationCode(r.Context(), database.CreateOAuthAuthorizationCodeParams{
		CodeHash:      codeHash[:],
		ClientID:      validated.Client.ID,
		UserID:        userID,
		RedirectUri:   validated.RedirectURI,
		CodeChallenge: validated.CodeChallenge,
		Scope:         validated.Scope,
		Resource:      validated.Resource,
	}); err != nil {
		internalError(w, "create oauth authorization code", err)
		return
	}
	redirectTo := buildOAuthRedirectURL(validated.RedirectURI, url.Values{
		"code": {rawCode},
		"iss":  {s.appConfig.PublicOrigin},
	}, validated.State)
	writeOAuthJSON(w, http.StatusOK, map[string]string{"redirect_to": redirectTo})
}

// oauthToken 实现 OAuth 2.1 令牌换取与刷新轮换：授权码单次原子消费并校验 PKCE S256，刷新令牌每次原子替换。
func (s *server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicOrigin(w) {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	r.Body = http.MaxBytesReader(w, r.Body, oauthMaxBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}
	clientIDText := strings.TrimSpace(r.PostForm.Get("client_id"))
	if clientIDText == "" {
		if basicClientID, _, ok := r.BasicAuth(); ok {
			clientIDText = strings.TrimSpace(basicClientID)
		}
	}
	clientID, err := uuid.Parse(clientIDText)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "invalid client_id")
		return
	}
	resource := strings.TrimRight(strings.TrimSpace(r.PostForm.Get("resource")), "/")

	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		code := r.PostForm.Get("code")
		redirectURI := r.PostForm.Get("redirect_uri")
		codeVerifier := r.PostForm.Get("code_verifier")
		if code == "" || redirectURI == "" || len(codeVerifier) < 43 || len(codeVerifier) > 128 {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code, redirect_uri, and valid code_verifier are required")
			return
		}
		codeHash := sha256.Sum256([]byte(code))
		row, err := s.queries.ConsumeOAuthAuthorizationCode(r.Context(), codeHash[:])
		if errors.Is(err, pgx.ErrNoRows) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid or expired")
			return
		}
		if err != nil {
			slog.Error("consume oauth authorization code failed", "error", err)
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "token exchange unavailable")
			return
		}
		if row.ClientID != clientID || row.RedirectUri != redirectURI {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id or redirect_uri does not match authorization code")
			return
		}
		if resource != "" && resource != row.Resource {
			writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource does not match authorization code")
			return
		}
		verifierSum := sha256.Sum256([]byte(codeVerifier))
		computedChallenge := base64.RawURLEncoding.EncodeToString(verifierSum[:])
		if subtle.ConstantTimeCompare([]byte(computedChallenge), []byte(row.CodeChallenge)) != 1 {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
			return
		}
		accessToken, accessHash := generateOpaqueToken()
		refreshToken, refreshHash := generateOpaqueToken()
		if err := s.queries.CreateOAuthToken(r.Context(), database.CreateOAuthTokenParams{
			ClientID:         row.ClientID,
			UserID:           row.UserID,
			Scope:            row.Scope,
			Resource:         row.Resource,
			AccessTokenHash:  accessHash[:],
			RefreshTokenHash: refreshHash[:],
		}); err != nil {
			slog.Error("create oauth token failed", "error", err)
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to issue token")
			return
		}
		writeOAuthJSON(w, http.StatusOK, map[string]any{
			"access_token":  accessToken,
			"token_type":    "Bearer",
			"expires_in":    oauthAccessTokenTTL,
			"refresh_token": refreshToken,
			"scope":         row.Scope,
		})
	case "refresh_token":
		oldRefreshToken := r.PostForm.Get("refresh_token")
		if oldRefreshToken == "" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
			return
		}
		oldRefreshHash := sha256.Sum256([]byte(oldRefreshToken))
		newAccessToken, newAccessHash := generateOpaqueToken()
		newRefreshToken, newRefreshHash := generateOpaqueToken()
		var rotated database.RotateOAuthTokenRow
		err := pgx.BeginFunc(r.Context(), s.databasePool, func(tx pgx.Tx) error {
			var err error
			rotated, err = database.New(tx).RotateOAuthToken(r.Context(), database.RotateOAuthTokenParams{
				NewAccessTokenHash:  newAccessHash[:],
				NewRefreshTokenHash: newRefreshHash[:],
				RefreshTokenHash:    oldRefreshHash[:],
				ClientID:            clientID,
			})
			if err != nil {
				return err
			}
			if resource != "" && resource != rotated.Resource {
				return errOAuthResourceMismatch
			}
			return nil
		})
		if errors.Is(err, pgx.ErrNoRows) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid or expired")
			return
		}
		if errors.Is(err, errOAuthResourceMismatch) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource does not match token")
			return
		}
		if err != nil {
			slog.Error("rotate oauth token failed", "error", err)
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "token refresh unavailable")
			return
		}
		writeOAuthJSON(w, http.StatusOK, map[string]any{
			"access_token":  newAccessToken,
			"token_type":    "Bearer",
			"expires_in":    oauthAccessTokenTTL,
			"refresh_token": newRefreshToken,
			"scope":         rotated.Scope,
		})
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code and refresh_token are supported")
	}
}

// oauthRevoke 实现 RFC 7009 令牌撤销：删除对应的访问/刷新令牌记录，按规范对未知令牌同样返回 200。
func (s *server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicOrigin(w) {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, oauthMaxBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}
	if token := r.PostForm.Get("token"); token != "" {
		tokenHash := sha256.Sum256([]byte(token))
		if err := s.queries.RevokeOAuthTokenByHash(r.Context(), tokenHash[:]); err != nil {
			slog.Error("revoke oauth token failed", "error", err)
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "revocation unavailable")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// listOAuthConnections 返回当前用户的远程记忆 MCP 地址和已授权的外部应用清单，供 /memories 弹窗展示。
func (s *server) listOAuthConnections(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	rows, err := s.queries.ListUserOAuthConnections(r.Context(), userID)
	if err != nil {
		internalError(w, "list user oauth connections", err)
		return
	}
	connections := make([]oauthConnectionSummary, 0, len(rows))
	for _, row := range rows {
		connections = append(connections, oauthConnectionSummary{
			ID:         row.ID,
			ClientName: row.ClientName,
			Scope:      row.Scope,
			CreatedAt:  row.CreatedAt,
			LastUsedAt: row.LastUsedAt,
		})
	}
	writeAdminJSON(w, struct {
		MCPURL      string                   `json:"mcp_url"`
		Connections []oauthConnectionSummary `json:"connections"`
	}{
		MCPURL:      s.mcpMemoryResourceURL(),
		Connections: connections,
	})
}

// deleteOAuthConnection 立即撤销当前用户的单条外部 MCP 授权连接。
func (s *server) deleteOAuthConnection(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	connectionID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "授权记录编号无效", http.StatusBadRequest)
		return
	}
	deleted, err := s.queries.DeleteUserOAuthConnection(r.Context(), database.DeleteUserOAuthConnectionParams{
		ID:     connectionID,
		UserID: userID,
	})
	if err != nil {
		internalError(w, "delete user oauth connection", err)
		return
	}
	if deleted == 0 {
		http.Error(w, "找不到该授权连接", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateAuthorizeParams 先核对客户端与已登记回调地址（绝不向未验证地址重定向），再检查其余 OAuth 2.1 参数。
func (s *server) validateAuthorizeParams(ctx context.Context, params oauthAuthorizeParams) (validatedAuthorizeRequest, string, string, error) {
	clientID, err := uuid.Parse(strings.TrimSpace(params.ClientID))
	if err != nil {
		return validatedAuthorizeRequest{}, "", "无效的客户端标识（client_id）", nil
	}
	client, err := s.queries.GetOAuthClient(ctx, clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return validatedAuthorizeRequest{}, "", "未找到对应的已注册客户端，请重新在外部应用中发起连接", nil
	}
	if err != nil {
		return validatedAuthorizeRequest{}, "", "", err
	}
	if !redirectURIMatches(client.RedirectUris, params.RedirectURI) {
		return validatedAuthorizeRequest{}, "", "回调地址（redirect_uri）与已注册客户端不匹配", nil
	}
	parsedRedirect, _ := url.Parse(params.RedirectURI)
	redirectError := func(code, description string) (validatedAuthorizeRequest, string, string, error) {
		target := buildOAuthRedirectURL(params.RedirectURI, url.Values{
			"error":             {code},
			"error_description": {description},
			"iss":               {s.appConfig.PublicOrigin},
		}, params.State)
		return validatedAuthorizeRequest{}, target, "", nil
	}
	if params.ResponseType != "code" {
		return redirectError("unsupported_response_type", "only response_type=code is supported")
	}
	if params.CodeChallengeMethod != "S256" || !isValidPKCEChallenge(params.CodeChallenge) {
		return redirectError("invalid_request", "PKCE S256 code_challenge is required")
	}
	scope := oauthMemoryScope
	if requested := strings.Fields(params.Scope); len(requested) > 0 {
		for _, item := range requested {
			if item != oauthMemoryScope {
				return redirectError("invalid_scope", "only scope=memory is supported")
			}
		}
	}
	expectedResource := s.mcpMemoryResourceURL()
	if requestedResource := strings.TrimRight(strings.TrimSpace(params.Resource), "/"); requestedResource != "" && requestedResource != expectedResource {
		return redirectError("invalid_target", "resource must match this MCP endpoint")
	}
	return validatedAuthorizeRequest{
		Client:        client,
		RedirectURI:   params.RedirectURI,
		RedirectHost:  parsedRedirect.Host,
		State:         params.State,
		Scope:         scope,
		CodeChallenge: params.CodeChallenge,
		Resource:      expectedResource,
	}, "", "", nil
}

// validateOAuthRedirectURI 仅放行公网 HTTPS 与本机回环 HTTP（供本地 CLI/桌面 MCP 客户端回调），拒绝带 fragment 或内嵌密码的地址。
func validateOAuthRedirectURI(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.Hostname() == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())
}

// redirectURIMatches 优先精确比对已注册回调；对于 RFC 8252 §7.3 本机回环 HTTP 回调，允许端口动态变化。
func redirectURIMatches(registered []string, candidate string) bool {
	if slices.Contains(registered, candidate) {
		return true
	}
	candURL, err := url.Parse(candidate)
	if err != nil || candURL.Scheme != "http" || !isLoopbackHost(candURL.Hostname()) || candURL.Fragment != "" || candURL.User != nil {
		return false
	}
	for _, entry := range registered {
		regURL, err := url.Parse(entry)
		if err != nil || regURL.Scheme != "http" || !isLoopbackHost(regURL.Hostname()) {
			continue
		}
		if strings.EqualFold(regURL.Hostname(), candURL.Hostname()) && regURL.Path == candURL.Path && regURL.RawQuery == candURL.RawQuery {
			return true
		}
	}
	return false
}

// isLoopbackHost 判断主机名是否为本机回环地址，匹配 localhost、127.0.0.1 与 IPv6 ::1。
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// isValidPKCEChallenge 校验 S256 code_challenge 长度与 Base64URL 字符集，防止注入异常参数。
func isValidPKCEChallenge(value string) bool {
	if len(value) < 43 || len(value) > 128 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil
}

// normalizeOAuthClientName 清理客户端展示名并限制长度，留空时回退到回调地址主机名。
func normalizeOAuthClientName(raw, fallbackRedirectURI string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		if parsed, err := url.Parse(fallbackRedirectURI); err == nil && parsed.Host != "" {
			name = parsed.Host
		} else {
			name = "MCP Client"
		}
	}
	runes := []rune(name)
	if len(runes) > oauthMaxClientNameLen {
		return string(runes[:oauthMaxClientNameLen])
	}
	return name
}

// buildOAuthRedirectURL 在保留回调原查询参数的前提下追加授权结果、iss 与非空 state。
func buildOAuthRedirectURL(redirectURI string, extra url.Values, state string) string {
	parsed, _ := url.Parse(redirectURI)
	query := parsed.Query()
	for key, values := range extra {
		for _, value := range values {
			query.Set(key, value)
		}
	}
	if state != "" {
		query.Set("state", state)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// writeOAuthJSON 输出禁用缓存的 OAuth JSON 响应，符合 RFC 6749 §5.1 对令牌响应的缓存约束。
func writeOAuthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeOAuthError 按 RFC 6749 错误体格式返回 OAuth 错误码与可读描述。
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeOAuthJSON(w, status, map[string]string{
		"error":             code,
		"error_description": description,
	})
}
