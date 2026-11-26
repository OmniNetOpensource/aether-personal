-- 本迁移新增通用的自定义模型供应商表，移除 service_settings 中的 ikun 渠道，并清理用户旧的 ikun 模型选择。

-- +goose Up
CREATE TABLE model_providers (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL UNIQUE CHECK (length(btrim(name)) BETWEEN 1 AND 64),
    protocol text NOT NULL CHECK (protocol IN ('openai', 'gemini', 'claude')),
    base_url text NOT NULL CHECK (length(base_url) BETWEEN 1 AND 2048),
    api_key_ciphertext bytea NOT NULL,
    key_suffix text NOT NULL DEFAULT '' CHECK (length(key_suffix) <= 4),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

DELETE FROM service_settings WHERE service IN ('ikun_gemini', 'ikun_claude');
ALTER TABLE service_settings DROP CONSTRAINT service_settings_service_check;
ALTER TABLE service_settings ADD CONSTRAINT service_settings_service_check
    CHECK (service IN ('openrouter', 'resend', 'serper', 'parallel', 'everyinfra', 'aliyun_oss', 'aliyun_fc'));

UPDATE user_model_settings
SET main_model = 'openrouter/free', main_context_length = NULL
WHERE main_model LIKE 'ikun:%';

UPDATE user_model_settings
SET subagent_model = NULL, subagent_context_length = NULL
WHERE subagent_model LIKE 'ikun:%';

UPDATE model_settings
SET main_model = 'openrouter/free'
WHERE main_model LIKE 'ikun:%';

UPDATE model_settings
SET subagent_model = NULL
WHERE subagent_model LIKE 'ikun:%';

ALTER TABLE user_model_settings DROP CONSTRAINT user_model_settings_main_model_check;
ALTER TABLE user_model_settings ADD CONSTRAINT user_model_settings_main_model_check
    CHECK (length(main_model) BETWEEN 1 AND 256);
ALTER TABLE user_model_settings DROP CONSTRAINT user_model_settings_subagent_model_check;
ALTER TABLE user_model_settings ADD CONSTRAINT user_model_settings_subagent_model_check
    CHECK (length(subagent_model) BETWEEN 1 AND 256);

-- +goose Down
UPDATE user_model_settings
SET main_model = 'openrouter/free', main_context_length = NULL
WHERE length(main_model) > 160;

UPDATE user_model_settings
SET subagent_model = NULL, subagent_context_length = NULL
WHERE length(subagent_model) > 160;

ALTER TABLE user_model_settings DROP CONSTRAINT user_model_settings_main_model_check;
ALTER TABLE user_model_settings ADD CONSTRAINT user_model_settings_main_model_check
    CHECK (length(main_model) BETWEEN 1 AND 160);
ALTER TABLE user_model_settings DROP CONSTRAINT user_model_settings_subagent_model_check;
ALTER TABLE user_model_settings ADD CONSTRAINT user_model_settings_subagent_model_check
    CHECK (length(subagent_model) BETWEEN 1 AND 160);

ALTER TABLE service_settings DROP CONSTRAINT service_settings_service_check;
ALTER TABLE service_settings ADD CONSTRAINT service_settings_service_check
    CHECK (service IN ('openrouter', 'ikun_gemini', 'ikun_claude', 'resend', 'serper', 'parallel', 'everyinfra', 'aliyun_oss', 'aliyun_fc'));

DROP TABLE model_providers;
