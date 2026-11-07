-- 本迁移增加独立压缩模型配置和 ikun Claude 凭证；既有部署未选择压缩模型时继续沿用 Main。

-- +goose Up
ALTER TABLE model_settings
    ADD COLUMN compact_model text CHECK (length(compact_model) BETWEEN 1 AND 160),
    ADD COLUMN compact_model_max_tokens integer CHECK (compact_model_max_tokens > 0),
    ADD COLUMN compact_context_length bigint CHECK (compact_context_length > 0);
ALTER TABLE service_settings DROP CONSTRAINT service_settings_service_check;
ALTER TABLE service_settings ADD CONSTRAINT service_settings_service_check
    CHECK (service IN ('openrouter', 'ikun_gemini', 'ikun_claude', 'resend', 'serper', 'parallel', 'everyinfra', 'aliyun_oss'));

-- +goose Down
DELETE FROM service_settings WHERE service = 'ikun_claude';
ALTER TABLE service_settings DROP CONSTRAINT service_settings_service_check;
ALTER TABLE service_settings ADD CONSTRAINT service_settings_service_check
    CHECK (service IN ('openrouter', 'ikun_gemini', 'resend', 'serper', 'parallel', 'everyinfra', 'aliyun_oss'));
ALTER TABLE model_settings DROP COLUMN compact_context_length,
    DROP COLUMN compact_model_max_tokens, DROP COLUMN compact_model;
