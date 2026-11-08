-- 本迁移给运行时设置加闲置反思的时长（分钟）：用户最后一次对话闲置这么久后，系统投一次反思触发；看板可改，默认 2 小时。

-- +goose Up
ALTER TABLE model_settings ADD COLUMN reflection_idle_minutes integer NOT NULL DEFAULT 120
    CHECK (reflection_idle_minutes BETWEEN 10 AND 10080);

-- +goose Down
ALTER TABLE model_settings DROP COLUMN reflection_idle_minutes;
