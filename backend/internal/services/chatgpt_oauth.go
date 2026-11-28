// 本文件实现部署级 Sign in with ChatGPT（OAuth 2.0 + PKCE + dynamic_agent_client）授权流、AES-GCM 凭证存取与跨实例并发安全刷新。

package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OmniNetOpensource/aether-personal/backend/internal/database"
	"github.com/OmniNetOpensource/aether-personal/backend/internal/llm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ChatGPT 标识部署级 ChatGPT 订阅模型渠道及其 OAuth 端点常量。
const (
	ChatGPT                  = "chatgpt"
	chatGPTAuthorizeEndpoint = "https://auth.openai.com/api/accounts/authorize"
	chatGPTTokenEndpoint     = "https://auth.openai.com/api/accounts/oauth/token"
	chatGPTAPIBaseURL        = "https://api.openai.com/v1"
	chatGPTRedirectURI       = "http://127.0.0.1:1455/auth/callback"
	chatGPTDynamicClientID   = "dynamic_agent_client"
	chatGPTAgentNameHint     = "aether"
	chatGPTScope             = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	chatGPTAccessRefreshSkew = 2 * time.Minute
)

var (
	chatGPTAccessTokenAAD  = []byte("chatgpt_oauth:access_token")
	chatGPTRefreshTokenAAD = []byte("chatgpt_oauth:refresh_token")
	chatGPTCodeVerifierAAD = []byte("chatgpt_oauth:code_verifier")
)

// ChatGPTStatusInfo 是管理看板展示的 ChatGPT 订阅连接脱敏状态，永不回显访问或刷新令牌。
type ChatGPTStatusInfo struct {
	Status          string     `json:"status"`
	AccountEmail    string     `json:"account_email"`
	AccountSub      string     `json:"account_sub"`
	AccessExpiresAt *time.Time `json:"access_expires_at"`
	UpdatedAt       *time.Time `json:"updated_at"`
}

// ChatGPTAuthStart 返回一次新发起的授权链接与回环回调地址，供管理员在浏览器完成同意后粘贴回看板。
type ChatGPTAuthStart struct {
	AuthorizeURL string `json:"authorize_url"`
	RedirectURI  string `json:"redirect_uri"`
}

// chatGPTTokenResponse 接收 OpenAI OAuth token 端点的换票或刷新响应。
type chatGPTTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// chatGPTIDClaims 提取 OpenAI id_token 载荷中的账号标识与 nonce。
type chatGPTIDClaims struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Nonce string `json:"nonce"`
}

// ChatGPTStatus 读取部署级 ChatGPT 订阅的脱敏连接状态，并检测密文当前能否用部署主密钥解开。
func (s *Store) ChatGPTStatus(ctx context.Context) (ChatGPTStatusInfo, error) {
	row, err := s.queries.GetChatGPTOAuth(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatGPTStatusInfo{Status: "not_configured"}, nil
	}
	if err != nil {
		return ChatGPTStatusInfo{}, err
	}
	return s.chatGPTStatusFromRow(row), nil
}

// chatGPTStatusFromRow 将数据库行转换为看板脱敏状态，并校验访问/刷新令牌密文可读性。
func (s *Store) chatGPTStatusFromRow(row database.ChatgptOauth) ChatGPTStatusInfo {
	updatedAt := row.UpdatedAt
	info := ChatGPTStatusInfo{
		Status:          "not_configured",
		AccountEmail:    row.AccountEmail,
		AccountSub:      row.AccountSub,
		AccessExpiresAt: row.AccessExpiresAt,
		UpdatedAt:       &updatedAt,
	}
	if row.ClientID == "" || len(row.AccessTokenCiphertext) == 0 || len(row.RefreshTokenCiphertext) == 0 {
		return info
	}
	if _, err := s.cipher.Open(nil, nil, row.AccessTokenCiphertext, chatGPTAccessTokenAAD); err != nil {
		info.Status = "unreadable"
		return info
	}
	if _, err := s.cipher.Open(nil, nil, row.RefreshTokenCiphertext, chatGPTRefreshTokenAAD); err != nil {
		info.Status = "unreadable"
		return info
	}
	info.Status = "connected"
	return info
}

// StartChatGPTAuth 生成一次带 PKCE、state 与 nonce 的 Sign in with ChatGPT 授权流并持久化到 PostgreSQL，供多实例安全完成回调。
func (s *Store) StartChatGPTAuth(ctx context.Context) (ChatGPTAuthStart, error) {
	row, err := s.queries.EnsureChatGPTHostID(ctx, "urn:uuid:"+uuid.NewString())
	if err != nil {
		return ChatGPTAuthStart{}, err
	}
	if err := s.queries.DeleteExpiredChatGPTOAuthFlows(ctx); err != nil {
		return ChatGPTAuthStart{}, err
	}
	state, err := randomURLToken(32)
	if err != nil {
		return ChatGPTAuthStart{}, err
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		return ChatGPTAuthStart{}, err
	}
	codeVerifier, err := randomURLToken(32)
	if err != nil {
		return ChatGPTAuthStart{}, err
	}
	challengeSum := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(challengeSum[:])
	stateSum := sha256.Sum256([]byte(state))

	// 首次连接走 dynamic_agent_client 动态注册；已有签发 client_id 时直接复用。
	initialClientID := chatGPTDynamicClientID
	if row.ClientID != "" {
		initialClientID = row.ClientID
	}
	if err := s.queries.CreateChatGPTOAuthFlow(ctx, database.CreateChatGPTOAuthFlowParams{
		StateHash:              stateSum[:],
		CodeVerifierCiphertext: s.cipher.Seal(nil, nil, []byte(codeVerifier), chatGPTCodeVerifierAAD),
		Nonce:                  nonce,
		RedirectUri:            chatGPTRedirectURI,
		InitialClientID:        initialClientID,
	}); err != nil {
		return ChatGPTAuthStart{}, err
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", initialClientID)
	params.Set("redirect_uri", chatGPTRedirectURI)
	params.Set("scope", chatGPTScope)
	params.Set("resource", chatGPTAPIBaseURL)
	params.Set("state", state)
	params.Set("nonce", nonce)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	if initialClientID == chatGPTDynamicClientID {
		params.Set("ext_agent_host_id", row.HostID)
		params.Set("agent_name_hint", chatGPTAgentNameHint)
	}
	return ChatGPTAuthStart{
		AuthorizeURL: chatGPTAuthorizeEndpoint + "?" + params.Encode(),
		RedirectURI:  chatGPTRedirectURI,
	}, nil
}

// CompleteChatGPTAuth 解析管理员粘贴的回环回调 URL，原子消费授权流并向 OpenAI 换取访问与刷新令牌。
func (s *Store) CompleteChatGPTAuth(ctx context.Context, rawCallbackURL string) (ChatGPTStatusInfo, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawCallbackURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ChatGPTStatusInfo{}, fmt.Errorf("%w: invalid callback URL", ErrInvalid)
	}
	query := parsed.Query()
	state := strings.TrimSpace(query.Get("state"))
	if state == "" {
		return ChatGPTStatusInfo{}, fmt.Errorf("%w: callback URL is missing state", ErrInvalid)
	}

	stateSum := sha256.Sum256([]byte(state))
	flow, err := s.queries.ConsumeChatGPTOAuthFlow(ctx, stateSum[:])
	if errors.Is(err, pgx.ErrNoRows) {
		return ChatGPTStatusInfo{}, fmt.Errorf("%w: authorization session expired or already used", ErrInvalid)
	}
	if err != nil {
		return ChatGPTStatusInfo{}, err
	}
	expectedRedirect, err := url.Parse(flow.RedirectUri)
	if err != nil || parsed.Scheme != expectedRedirect.Scheme || parsed.Host != expectedRedirect.Host || parsed.Path != expectedRedirect.Path {
		return ChatGPTStatusInfo{}, fmt.Errorf("%w: callback URL does not match redirect_uri", ErrInvalid)
	}
	if oauthErr := strings.TrimSpace(query.Get("error")); oauthErr != "" {
		return ChatGPTStatusInfo{}, fmt.Errorf("%w: OpenAI rejected authorization (%s)", ErrInvalid, oauthErr)
	}
	code := strings.TrimSpace(query.Get("code"))
	if code == "" {
		return ChatGPTStatusInfo{}, fmt.Errorf("%w: callback URL is missing authorization code", ErrInvalid)
	}

	// 动态注册必须在回调中带回签发的 oaiapp_... client_id；换票与后续刷新都不能再传 dynamic_agent_client。
	clientID := strings.TrimSpace(query.Get("client_id"))
	if clientID == "" {
		if flow.InitialClientID == chatGPTDynamicClientID {
			return ChatGPTStatusInfo{}, fmt.Errorf("%w: callback URL is missing issued client_id", ErrInvalid)
		}
		clientID = flow.InitialClientID
	}
	if clientID == chatGPTDynamicClientID || len(clientID) > 256 {
		return ChatGPTStatusInfo{}, fmt.Errorf("%w: invalid issued client_id", ErrInvalid)
	}

	codeVerifierBytes, err := s.cipher.Open(nil, nil, flow.CodeVerifierCiphertext, chatGPTCodeVerifierAAD)
	if err != nil {
		return ChatGPTStatusInfo{}, fmt.Errorf("chatgpt_oauth: %w", ErrUnreadable)
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", flow.RedirectUri)
	form.Set("client_id", clientID)
	form.Set("code_verifier", string(codeVerifierBytes))
	form.Set("resource", chatGPTAPIBaseURL)

	tokens, err := s.exchangeChatGPTToken(ctx, form)
	if err != nil {
		return ChatGPTStatusInfo{}, err
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		return ChatGPTStatusInfo{}, fmt.Errorf("%w: OpenAI token response is missing access_token or refresh_token", ErrUpstream)
	}

	var accountSub, accountEmail string
	if tokens.IDToken != "" {
		claims, err := parseJWTClaims(tokens.IDToken)
		if err != nil {
			return ChatGPTStatusInfo{}, fmt.Errorf("%w: invalid id_token payload", ErrUpstream)
		}
		if claims.Nonce != "" && claims.Nonce != flow.Nonce {
			return ChatGPTStatusInfo{}, fmt.Errorf("%w: id_token nonce mismatch", ErrInvalid)
		}
		accountSub = truncateString(strings.TrimSpace(claims.Sub), 256)
		accountEmail = truncateString(strings.TrimSpace(claims.Email), 320)
	}

	expiresAt := tokenExpiryTime(tokens.ExpiresIn)
	row, err := s.queries.SaveChatGPTOAuthTokens(ctx, database.SaveChatGPTOAuthTokensParams{
		ClientID:               clientID,
		AccountSub:             accountSub,
		AccountEmail:           accountEmail,
		AccessTokenCiphertext:  s.cipher.Seal(nil, nil, []byte(tokens.AccessToken), chatGPTAccessTokenAAD),
		RefreshTokenCiphertext: s.cipher.Seal(nil, nil, []byte(tokens.RefreshToken), chatGPTRefreshTokenAAD),
		AccessExpiresAt:        &expiresAt,
	})
	if err != nil {
		return ChatGPTStatusInfo{}, err
	}
	return s.chatGPTStatusFromRow(row), nil
}

// DisconnectChatGPT 清除已保存的 ChatGPT 令牌、client_id、账号信息与所有待完成授权流，仅保留持久化 host_id。
func (s *Store) DisconnectChatGPT(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := database.New(tx)
		if err := q.ClearChatGPTOAuth(ctx); err != nil {
			return err
		}
		return q.DeleteAllChatGPTOAuthFlows(ctx)
	})
}

// ChatGPTConfig 返回可用于调用 OpenAI Responses API 的运行时 llm.Config；access_token 临近过期时在行锁保护下自动轮换刷新。
func (s *Store) ChatGPTConfig(ctx context.Context) (llm.Config, error) {
	row, err := s.queries.GetChatGPTOAuth(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return llm.Config{}, fmt.Errorf("%s: %w", ChatGPT, ErrNotConfigured)
	}
	if err != nil {
		return llm.Config{}, err
	}
	if row.ClientID == "" || len(row.AccessTokenCiphertext) == 0 || len(row.RefreshTokenCiphertext) == 0 {
		return llm.Config{}, fmt.Errorf("%s: %w", ChatGPT, ErrNotConfigured)
	}
	if row.AccessExpiresAt != nil && time.Until(*row.AccessExpiresAt) > chatGPTAccessRefreshSkew {
		accessToken, err := s.cipher.Open(nil, nil, row.AccessTokenCiphertext, chatGPTAccessTokenAAD)
		if err != nil {
			return llm.Config{}, fmt.Errorf("%s: %w", ChatGPT, ErrUnreadable)
		}
		return chatGPTLLMConfig(string(accessToken)), nil
	}

	// 在事务内锁住唯一配置行，串行化 api-1 / api-2 的并发刷新，避免旧 refresh_token 被重复使用导致授权作废。
	var freshAccessToken string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		txQueries := database.New(tx)
		locked, err := txQueries.LockChatGPTOAuth(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%s: %w", ChatGPT, ErrNotConfigured)
		}
		if err != nil {
			return err
		}
		if locked.ClientID == "" || len(locked.AccessTokenCiphertext) == 0 || len(locked.RefreshTokenCiphertext) == 0 {
			return fmt.Errorf("%s: %w", ChatGPT, ErrNotConfigured)
		}
		// 拿到行锁后二次检查：若并发请求已先一步刷好新令牌，直接解密使用。
		if locked.AccessExpiresAt != nil && time.Until(*locked.AccessExpiresAt) > chatGPTAccessRefreshSkew {
			token, err := s.cipher.Open(nil, nil, locked.AccessTokenCiphertext, chatGPTAccessTokenAAD)
			if err != nil {
				return fmt.Errorf("%s: %w", ChatGPT, ErrUnreadable)
			}
			freshAccessToken = string(token)
			return nil
		}
		refreshTokenBytes, err := s.cipher.Open(nil, nil, locked.RefreshTokenCiphertext, chatGPTRefreshTokenAAD)
		if err != nil {
			return fmt.Errorf("%s: %w", ChatGPT, ErrUnreadable)
		}

		form := url.Values{}
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", string(refreshTokenBytes))
		form.Set("client_id", locked.ClientID)
		form.Set("resource", chatGPTAPIBaseURL)

		tokens, err := s.exchangeChatGPTToken(ctx, form)
		if err != nil {
			return err
		}
		if tokens.AccessToken == "" {
			return fmt.Errorf("%w: OpenAI refresh response is missing access_token", ErrUpstream)
		}
		nextRefreshToken := string(refreshTokenBytes)
		if tokens.RefreshToken != "" {
			nextRefreshToken = tokens.RefreshToken
		}
		accountSub, accountEmail := locked.AccountSub, locked.AccountEmail
		if tokens.IDToken != "" {
			if claims, err := parseJWTClaims(tokens.IDToken); err == nil {
				if sub := strings.TrimSpace(claims.Sub); sub != "" {
					accountSub = truncateString(sub, 256)
				}
				if email := strings.TrimSpace(claims.Email); email != "" {
					accountEmail = truncateString(email, 320)
				}
			}
		}

		expiresAt := tokenExpiryTime(tokens.ExpiresIn)
		if _, err := txQueries.SaveChatGPTOAuthTokens(ctx, database.SaveChatGPTOAuthTokensParams{
			ClientID:               locked.ClientID,
			AccountSub:             accountSub,
			AccountEmail:           accountEmail,
			AccessTokenCiphertext:  s.cipher.Seal(nil, nil, []byte(tokens.AccessToken), chatGPTAccessTokenAAD),
			RefreshTokenCiphertext: s.cipher.Seal(nil, nil, []byte(nextRefreshToken), chatGPTRefreshTokenAAD),
			AccessExpiresAt:        &expiresAt,
		}); err != nil {
			return err
		}
		freshAccessToken = tokens.AccessToken
		return nil
	})
	if err != nil {
		return llm.Config{}, err
	}
	return chatGPTLLMConfig(freshAccessToken), nil
}

// chatGPTLLMConfig 构造 ChatGPT 订阅渠道指向 OpenAI Responses API 的统一配置。
func chatGPTLLMConfig(accessToken string) llm.Config {
	return llm.Config{
		Protocol:      llm.ProtocolOpenAIResponses,
		BaseURL:       chatGPTAPIBaseURL,
		APIKey:        accessToken,
		SendSessionID: true,
	}
}

// exchangeChatGPTToken 向 OpenAI OAuth token 端点提交表单换票或刷新请求，禁止跟随重定向以防泄露凭证。
func (s *Store) exchangeChatGPTToken(ctx context.Context, form url.Values) (chatGPTTokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, chatGPTTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return chatGPTTokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "aether")

	client := *s.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return chatGPTTokenResponse{}, fmt.Errorf("%w: call OpenAI token endpoint: %w", ErrUpstream, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return chatGPTTokenResponse{}, fmt.Errorf("%w: read OpenAI token response: %w", ErrUpstream, err)
	}
	if response.StatusCode != http.StatusOK {
		var oauthErr struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if json.Unmarshal(body, &oauthErr) == nil && oauthErr.Error != "" {
			return chatGPTTokenResponse{}, fmt.Errorf("%w: OpenAI token endpoint returned HTTP %d (%s)", ErrUpstream, response.StatusCode, oauthErr.Error)
		}
		return chatGPTTokenResponse{}, fmt.Errorf("%w: OpenAI token endpoint returned HTTP %d", ErrUpstream, response.StatusCode)
	}
	var parsed chatGPTTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return chatGPTTokenResponse{}, fmt.Errorf("%w: decode OpenAI token response: %w", ErrUpstream, err)
	}
	return parsed, nil
}

// parseJWTClaims 解码来自 OpenAI TLS 令牌端点的 id_token 载荷部分，供校验 nonce 与读取账号展示信息。
func parseJWTClaims(idToken string) (chatGPTIDClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return chatGPTIDClaims{}, errors.New("malformed JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return chatGPTIDClaims{}, err
	}
	var claims chatGPTIDClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return chatGPTIDClaims{}, err
	}
	return claims, nil
}

// randomURLToken 生成指定随机字节长度的 URL-safe Base64 随机串，供 state、nonce 与 PKCE code_verifier 使用。
func randomURLToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// tokenExpiryTime 按 OpenAI 返回的 expires_in 秒数计算过期时间，缺省回退为 1 小时。
func tokenExpiryTime(expiresIn int64) time.Time {
	if expiresIn <= 0 {
		return time.Now().UTC().Add(time.Hour)
	}
	return time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
}

// truncateString 将字符串截断到指定字节长度以内，避免外部展示字段超出表约束。
func truncateString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
