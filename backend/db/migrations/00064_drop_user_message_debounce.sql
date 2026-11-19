-- 移除用户消息发前防抖配置；保留发前占位与发出后不中断任何请求，单发消息零延迟处理。
-- +goose Up
ALTER TABLE model_settings
    DROP COLUMN user_message_debounce_seconds;

-- +goose Down
ALTER TABLE model_settings
    ADD COLUMN user_message_debounce_seconds integer NOT NULL DEFAULT 15 CHECK (user_message_debounce_seconds BETWEEN 0 AND 300);
