-- 将热压缩默认等待时间由 159 秒下调为 75 秒，使大上下文回复后的热压缩落在约 2 分钟缓存存活窗口内；仅更新仍沿用旧默认值的配置行。
-- +goose Up
ALTER TABLE model_settings
    ALTER COLUMN hot_compact_delay_seconds SET DEFAULT 75;
UPDATE model_settings
SET hot_compact_delay_seconds = 75
WHERE hot_compact_delay_seconds = 159;

-- +goose Down
ALTER TABLE model_settings
    ALTER COLUMN hot_compact_delay_seconds SET DEFAULT 159;
UPDATE model_settings
SET hot_compact_delay_seconds = 159
WHERE hot_compact_delay_seconds = 75;
