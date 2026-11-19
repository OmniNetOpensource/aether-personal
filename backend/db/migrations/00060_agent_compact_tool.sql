-- 压缩沿用发起步骤的模型和输入边界；移除账号与旧全局配置中的独立压缩模型。
-- +goose Up
ALTER TABLE steps ADD COLUMN input_sequence bigint NOT NULL DEFAULT 0;
ALTER TABLE user_model_settings DROP COLUMN compact_model;
ALTER TABLE model_settings DROP COLUMN compact_model, DROP COLUMN compact_model_max_tokens, DROP COLUMN compact_context_length;

-- +goose Down
ALTER TABLE model_settings
    ADD COLUMN compact_model text CHECK (length(compact_model) BETWEEN 1 AND 160),
    ADD COLUMN compact_model_max_tokens integer CHECK (compact_model_max_tokens > 0),
    ADD COLUMN compact_context_length bigint CHECK (compact_context_length > 0);
ALTER TABLE user_model_settings ADD COLUMN compact_model text CHECK (length(compact_model) BETWEEN 1 AND 160);
ALTER TABLE steps DROP COLUMN input_sequence;
