-- 收到用户消息后先在发送模型请求前等待可配置的静默窗口，合并短时间连续补发，默认 15 秒；设 0 为不等待。
-- +goose Up
ALTER TABLE model_settings
    ADD COLUMN user_message_debounce_seconds integer NOT NULL DEFAULT 15 CHECK (user_message_debounce_seconds BETWEEN 0 AND 300);

-- +goose Down
ALTER TABLE model_settings
    DROP COLUMN user_message_debounce_seconds;
