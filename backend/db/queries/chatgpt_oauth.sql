-- 本文件定义部署级 ChatGPT OAuth 订阅凭据与跨实例授权流状态的数据库查询（sqlc 据此生成代码）。

-- name: GetChatGPTOAuth :one
SELECT id, host_id, client_id, account_sub, account_email,
       access_token_ciphertext, refresh_token_ciphertext, access_expires_at, updated_at
FROM chatgpt_oauth
WHERE id = 1;

-- name: LockChatGPTOAuth :one
-- 在事务内锁住唯一配置行，串行化跨实例的 refresh_token 轮换，避免旧票被重复刷新作废。
SELECT id, host_id, client_id, account_sub, account_email,
       access_token_ciphertext, refresh_token_ciphertext, access_expires_at, updated_at
FROM chatgpt_oauth
WHERE id = 1
FOR UPDATE;

-- name: EnsureChatGPTHostID :one
-- 首次发起授权时写入持久化 host_id，已存在则保留原值并返回当前整行状态。
INSERT INTO chatgpt_oauth (id, host_id)
VALUES (1, $1)
ON CONFLICT (id) DO UPDATE SET host_id = chatgpt_oauth.host_id
RETURNING id, host_id, client_id, account_sub, account_email,
          access_token_ciphertext, refresh_token_ciphertext, access_expires_at, updated_at;

-- name: SaveChatGPTOAuthTokens :one
UPDATE chatgpt_oauth
SET client_id = $1,
    account_sub = $2,
    account_email = $3,
    access_token_ciphertext = $4,
    refresh_token_ciphertext = $5,
    access_expires_at = $6,
    updated_at = now()
WHERE id = 1
RETURNING id, host_id, client_id, account_sub, account_email,
          access_token_ciphertext, refresh_token_ciphertext, access_expires_at, updated_at;

-- name: ClearChatGPTOAuth :exec
-- 断开连接时清空令牌、动态 client_id 与账号展示信息，保留持久化 host_id。
UPDATE chatgpt_oauth
SET client_id = '',
    account_sub = '',
    account_email = '',
    access_token_ciphertext = NULL,
    refresh_token_ciphertext = NULL,
    access_expires_at = NULL,
    updated_at = now()
WHERE id = 1;

-- name: DeleteExpiredChatGPTOAuthFlows :exec
DELETE FROM chatgpt_oauth_flows
WHERE expires_at <= now();

-- name: DeleteAllChatGPTOAuthFlows :exec
DELETE FROM chatgpt_oauth_flows;

-- name: CreateChatGPTOAuthFlow :exec
INSERT INTO chatgpt_oauth_flows (
    state_hash, code_verifier_ciphertext, nonce, redirect_uri, initial_client_id, expires_at
)
VALUES ($1, $2, $3, $4, $5, now() + interval '15 minutes');

-- name: ConsumeChatGPTOAuthFlow :one
-- 原子消费未过期的授权流，确保同一 state 只能完成一次换票。
DELETE FROM chatgpt_oauth_flows
WHERE state_hash = $1 AND expires_at > now()
RETURNING code_verifier_ciphertext, nonce, redirect_uri, initial_client_id;
