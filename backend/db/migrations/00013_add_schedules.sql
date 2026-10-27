-- +goose Up
CREATE TABLE schedules (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 160),
    context text NOT NULL CHECK (length(btrim(context)) BETWEEN 1 AND 24000),
    run_at timestamptz,
    cron text,
    timezone text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'triggered')),
    next_run_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((run_at IS NOT NULL AND cron IS NULL) OR (run_at IS NULL AND cron IS NOT NULL)),
    CHECK ((status = 'active' AND next_run_at IS NOT NULL)
        OR (status IN ('paused', 'triggered') AND next_run_at IS NULL)),
    CHECK (status <> 'triggered' OR run_at IS NOT NULL)
);

CREATE INDEX schedules_user_id_idx ON schedules (user_id, id);
CREATE INDEX schedules_due_idx ON schedules (next_run_at, id) WHERE status = 'active';

CREATE TABLE schedule_occurrences (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    schedule_id uuid NOT NULL REFERENCES schedules (id) ON DELETE CASCADE,
    schedule_version bigint NOT NULL,
    scheduled_for timestamptz NOT NULL,
    delivered_at timestamptz NOT NULL,
    context text NOT NULL,
    trigger_message_id uuid NOT NULL UNIQUE REFERENCES messages (id) ON DELETE CASCADE,
    UNIQUE (schedule_id, schedule_version, scheduled_for)
);

-- +goose Down
DROP TABLE schedule_occurrences;
DROP TABLE schedules;
