-- 本迁移保存服务凭证的密文与非敏感配套配置；清除 Key 保留记录，防止旧环境变量重新导入。

-- +goose Up
CREATE TABLE service_settings (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    service text NOT NULL UNIQUE CHECK (service IN ('openrouter', 'ikun_gemini', 'resend', 'serper', 'parallel', 'everyinfra')),
    api_key_ciphertext bytea,
    key_suffix text NOT NULL DEFAULT '' CHECK (length(key_suffix) <= 4),
    base_url text NOT NULL DEFAULT '',
    email_from text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE service_settings;
