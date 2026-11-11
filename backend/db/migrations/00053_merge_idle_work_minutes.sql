-- 本迁移把三种闲置后台工作的时长合并回单一的 reflection_idle_minutes（分钟，默认 120，看板可调）。

-- +goose Up
ALTER TABLE model_settings
    ADD COLUMN reflection_idle_minutes integer NOT NULL DEFAULT 120
        CHECK (reflection_idle_minutes BETWEEN 10 AND 10080);

UPDATE model_settings SET reflection_idle_minutes = recent_followup_idle_minutes;

ALTER TABLE model_settings
    DROP COLUMN memory_update_idle_minutes,
    DROP COLUMN recent_followup_idle_minutes,
    DROP COLUMN unknown_unknowns_idle_minutes;

-- +goose Down
ALTER TABLE model_settings
    ADD COLUMN memory_update_idle_minutes integer NOT NULL DEFAULT 60
        CHECK (memory_update_idle_minutes BETWEEN 10 AND 10080),
    ADD COLUMN recent_followup_idle_minutes integer NOT NULL DEFAULT 120
        CHECK (recent_followup_idle_minutes BETWEEN 10 AND 10080),
    ADD COLUMN unknown_unknowns_idle_minutes integer NOT NULL DEFAULT 180
        CHECK (unknown_unknowns_idle_minutes BETWEEN 10 AND 10080);

UPDATE model_settings SET recent_followup_idle_minutes = reflection_idle_minutes;

ALTER TABLE model_settings
    DROP COLUMN reflection_idle_minutes;
