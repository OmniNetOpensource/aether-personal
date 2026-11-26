-- 扩充 login_rate_limits.scope 约束以放行 OAuth 动态客户端注册的按 IP 与全站限流计数。
-- +goose Up
ALTER TABLE login_rate_limits DROP CONSTRAINT login_rate_limits_scope_check;
ALTER TABLE login_rate_limits ADD CONSTRAINT login_rate_limits_scope_check
    CHECK (scope IN (
        'challenge_global',
        'challenge_ip_minute',
        'challenge_ip_hour',
        'mail_email_day',
        'mail_global_minute',
        'mail_global_day',
        'oauth_register_ip_hour',
        'oauth_register_global_hour'
    ));

-- +goose Down
DELETE FROM login_rate_limits
WHERE scope IN ('oauth_register_ip_hour', 'oauth_register_global_hour');
ALTER TABLE login_rate_limits DROP CONSTRAINT login_rate_limits_scope_check;
ALTER TABLE login_rate_limits ADD CONSTRAINT login_rate_limits_scope_check
    CHECK (scope IN (
        'challenge_global',
        'challenge_ip_minute',
        'challenge_ip_hour',
        'mail_email_day',
        'mail_global_minute',
        'mail_global_day'
    ));
