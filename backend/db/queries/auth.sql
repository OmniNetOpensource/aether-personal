-- 本文件是用户认证的查询集合：保存与核销邮箱验证码、按邮箱建号、以及登录会话的创建与续期（sqlc 据此生成代码）。

-- name: SaveLoginCode :execrows
-- 同一邮箱 60 秒内重复请求时不覆盖，返回 0 行。
INSERT INTO login_codes (email, code_hash, expires_at)
VALUES ($1, $2, now() + interval '10 minutes')
ON CONFLICT (email) DO UPDATE
SET code_hash = EXCLUDED.code_hash,
    attempts = 0,
    expires_at = EXCLUDED.expires_at,
    created_at = now()
WHERE login_codes.created_at < now() - interval '60 seconds';

-- name: UseLoginCodeAttempt :one
-- 先占用一次尝试机会再比对，并发请求也不会超过 5 次。
UPDATE login_codes
SET attempts = attempts + 1
WHERE email = $1 AND expires_at > now() AND attempts < 5
RETURNING code_hash;

-- name: DeleteLoginCode :exec
DELETE FROM login_codes WHERE email = $1;

-- name: UpsertUserByEmail :one
INSERT INTO users (email)
VALUES ($1)
ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
RETURNING id;

-- name: CreateSession :exec
INSERT INTO sessions (user_id, token_hash, expires_at)
VALUES ($1, $2, now() + interval '30 days');

-- name: GetActiveSession :one
SELECT sessions.id, sessions.user_id, users.email FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = $1 AND sessions.expires_at > now();

-- name: ExtendSession :exec
-- 剩余不足 29 天（距上次延期超过一天）才写库。
UPDATE sessions
SET expires_at = now() + interval '30 days'
WHERE id = $1 AND expires_at < now() + interval '29 days';

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;
