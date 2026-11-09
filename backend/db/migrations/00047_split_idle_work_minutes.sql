-- 本迁移把闲置反思的单一时长拆成三种后台工作各自的闲置时长（分钟）：记忆整理、近期跟进、未知的未知；看板可改，默认 1、2、3 小时。

-- +goose Up
ALTER TABLE model_settings
    ADD COLUMN memory_update_idle_minutes integer NOT NULL DEFAULT 60
        CHECK (memory_update_idle_minutes BETWEEN 10 AND 10080),
    ADD COLUMN recent_followup_idle_minutes integer NOT NULL DEFAULT 120
        CHECK (recent_followup_idle_minutes BETWEEN 10 AND 10080),
    ADD COLUMN unknown_unknowns_idle_minutes integer NOT NULL DEFAULT 180
        CHECK (unknown_unknowns_idle_minutes BETWEEN 10 AND 10080),
    DROP COLUMN reflection_idle_minutes;

-- +goose Down
ALTER TABLE model_settings
    ADD COLUMN reflection_idle_minutes integer NOT NULL DEFAULT 120
        CHECK (reflection_idle_minutes BETWEEN 10 AND 10080),
    DROP COLUMN memory_update_idle_minutes,
    DROP COLUMN recent_followup_idle_minutes,
    DROP COLUMN unknown_unknowns_idle_minutes;
