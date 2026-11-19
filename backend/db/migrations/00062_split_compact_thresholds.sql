-- 热压缩和主动提醒分别设定输入 token 阈值，已有部署保留管理员填写的主动提醒线。
-- +goose Up
ALTER TABLE model_settings
    ADD COLUMN hot_compact_at_tokens integer NOT NULL DEFAULT 60000 CHECK (hot_compact_at_tokens BETWEEN 1000 AND 2000000),
    ALTER COLUMN compact_at_tokens SET DEFAULT 100000;

-- +goose Down
ALTER TABLE model_settings
    DROP COLUMN hot_compact_at_tokens,
    ALTER COLUMN compact_at_tokens SET DEFAULT 200000;
