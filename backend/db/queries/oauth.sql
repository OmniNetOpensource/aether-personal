-- 本文件定义 MCP OAuth 2.1 授权流的数据库查询：动态注册客户端、单次消费授权码、令牌签发轮换与用户侧撤销管理。

-- name: CreateOAuthClient :one
INSERT INTO oauth_clients (client_name, redirect_uris)
VALUES ($1, $2)
RETURNING id, client_name, redirect_uris, created_at;

-- name: GetOAuthClient :one
SELECT id, client_name, redirect_uris, created_at
FROM oauth_clients
WHERE id = $1;

-- name: CreateOAuthAuthorizationCode :exec
INSERT INTO oauth_authorization_codes (
    code_hash, client_id, user_id, redirect_uri, code_challenge, scope, resource, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, now() + interval '5 minutes');

-- name: ConsumeOAuthAuthorizationCode :one
-- 原子删除并返回未过期记录，确保同一授权码在并发换票时也只能成功使用一次。
DELETE FROM oauth_authorization_codes
WHERE code_hash = $1 AND expires_at > now()
RETURNING client_id, user_id, redirect_uri, code_challenge, scope, resource;

-- name: DeleteExpiredOAuthAuthorizationCodes :exec
DELETE FROM oauth_authorization_codes
WHERE expires_at <= now();

-- name: CreateOAuthToken :exec
INSERT INTO oauth_tokens (
    client_id, user_id, scope, resource,
    access_token_hash, access_expires_at,
    refresh_token_hash, refresh_expires_at
)
VALUES (
    $1, $2, $3, $4,
    $5, now() + interval '1 hour',
    $6, now() + interval '90 days'
);

-- name: GetActiveOAuthAccessToken :one
SELECT id, client_id, user_id, scope, resource, access_expires_at
FROM oauth_tokens
WHERE access_token_hash = $1 AND access_expires_at > now();

-- name: TouchOAuthTokenLastUsed :exec
-- 距上次记录超过 5 分钟才更新，避免高频 MCP 只读请求频繁写库。
UPDATE oauth_tokens
SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '5 minutes');

-- name: RotateOAuthToken :one
-- 在单条更新中原子轮换访问与刷新令牌哈希，防止同一刷新令牌被并发重复换票。
UPDATE oauth_tokens
SET access_token_hash = sqlc.arg(new_access_token_hash),
    access_expires_at = now() + interval '1 hour',
    refresh_token_hash = sqlc.arg(new_refresh_token_hash),
    refresh_expires_at = now() + interval '90 days',
    last_used_at = now()
WHERE refresh_token_hash = sqlc.arg(refresh_token_hash)
  AND client_id = sqlc.arg(client_id)
  AND refresh_expires_at > now()
RETURNING id, user_id, scope, resource;

-- name: RevokeOAuthTokenByHash :exec
DELETE FROM oauth_tokens
WHERE access_token_hash = $1 OR refresh_token_hash = $1;

-- name: ListUserOAuthConnections :many
SELECT t.id, t.client_id, c.client_name, t.scope, t.created_at, t.last_used_at
FROM oauth_tokens AS t
JOIN oauth_clients AS c ON c.id = t.client_id
WHERE t.user_id = $1 AND t.refresh_expires_at > now()
ORDER BY t.created_at DESC, t.id DESC;

-- name: DeleteUserOAuthConnection :execrows
DELETE FROM oauth_tokens
WHERE id = $1 AND user_id = $2;
