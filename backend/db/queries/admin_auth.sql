-- name: UseAdminLoginAttempt :execrows
INSERT INTO admin_login_attempts (singleton, attempts, window_started_at)
VALUES (true, 1, now())
ON CONFLICT (singleton) DO UPDATE
SET attempts = CASE WHEN admin_login_attempts.window_started_at <= now() - interval '1 minute'
        THEN 1 ELSE admin_login_attempts.attempts + 1 END,
    window_started_at = CASE WHEN admin_login_attempts.window_started_at <= now() - interval '1 minute'
        THEN now() ELSE admin_login_attempts.window_started_at END
WHERE admin_login_attempts.attempts < 10
    OR admin_login_attempts.window_started_at <= now() - interval '1 minute';

-- name: CreateAdminSession :exec
WITH expired AS (
    DELETE FROM admin_sessions
    WHERE expires_at <= now() OR credential_hash <> sqlc.arg(credential_hash)::bytea
)
INSERT INTO admin_sessions (token_hash, credential_hash)
VALUES (sqlc.arg(token_hash), sqlc.arg(credential_hash));

-- name: GetAdminSession :one
SELECT id FROM admin_sessions
WHERE token_hash = $1 AND credential_hash = $2 AND expires_at > now();

-- name: DeleteAdminSession :exec
DELETE FROM admin_sessions WHERE id = $1;
