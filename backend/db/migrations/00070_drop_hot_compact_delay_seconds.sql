-- 热压缩改为达到阈值并在最终回答落库后立即在同一租约内执行，不再配置等待秒数。
-- +goose Up
ALTER TABLE model_settings
    DROP COLUMN hot_compact_delay_seconds;

-- +goose Down
ALTER TABLE model_settings
    ADD COLUMN hot_compact_delay_seconds integer NOT NULL DEFAULT 75
        CHECK (hot_compact_delay_seconds > 0);
