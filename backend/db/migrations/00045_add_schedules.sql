-- 本迁移恢复定时安排：Main 用 schedule 工具保存一次性或周期安排，到期由调度器把保存的上下文投进对话；一次性安排投递后删除，周期安排推进下一次时间。

-- +goose Up
CREATE TABLE schedules (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 160),
    context text NOT NULL CHECK (length(btrim(context)) BETWEEN 1 AND 24000),
    cron text,
    timezone text,
    next_run_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((cron IS NULL) = (timezone IS NULL))
);

CREATE INDEX schedules_user_id_idx ON schedules (user_id, next_run_at);
CREATE INDEX schedules_due_idx ON schedules (next_run_at);

-- +goose Down
DROP TABLE schedules;
