-- 移除闲置后台反思配置项；历史消息表中的旧反思触发记录保留不变。
-- +goose Up
ALTER TABLE model_settings
    DROP COLUMN reflection_idle_minutes;

-- +goose Down
ALTER TABLE model_settings
    ADD COLUMN reflection_idle_minutes integer NOT NULL DEFAULT 120
        CHECK (reflection_idle_minutes BETWEEN 10 AND 10080);
