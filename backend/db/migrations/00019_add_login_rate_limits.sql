-- 本迁移保存跨实例共享的验证码防刷计数，重连或重启不会重置发信保护。

-- +goose Up
CREATE TABLE login_rate_limits (
    scope text NOT NULL CHECK (scope IN ('challenge_global', 'challenge_ip_minute', 'challenge_ip_hour', 'mail_email_day', 'mail_global_minute', 'mail_global_day')),
    key_hash bytea NOT NULL,
    attempts integer NOT NULL CHECK (attempts > 0),
    window_started_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key_hash)
);
CREATE INDEX login_rate_limits_window ON login_rate_limits (window_started_at);

-- +goose Down
DROP TABLE login_rate_limits;
