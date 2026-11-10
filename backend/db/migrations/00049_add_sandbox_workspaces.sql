-- 本迁移保存每位用户独立的 AgenticFS 入口及当前计算实例；释放计算实例不删除长期工作区。

-- +goose Up
CREATE TABLE sandbox_workspaces (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    server_addr text NOT NULL UNIQUE,
    role_arn text NOT NULL UNIQUE,
    sandbox_id text,
    token_ciphertext bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((sandbox_id IS NULL) = (token_ciphertext IS NULL))
);
ALTER TABLE service_settings DROP CONSTRAINT service_settings_service_check;
ALTER TABLE service_settings ADD CONSTRAINT service_settings_service_check
    CHECK (service IN ('openrouter', 'ikun_gemini', 'ikun_claude', 'resend', 'serper', 'parallel', 'everyinfra', 'aliyun_oss', 'aliyun_fc'));

-- +goose Down
DROP TABLE sandbox_workspaces;
DELETE FROM service_settings WHERE service = 'aliyun_fc';
ALTER TABLE service_settings DROP CONSTRAINT service_settings_service_check;
ALTER TABLE service_settings ADD CONSTRAINT service_settings_service_check
    CHECK (service IN ('openrouter', 'ikun_gemini', 'ikun_claude', 'resend', 'serper', 'parallel', 'everyinfra', 'aliyun_oss'));
