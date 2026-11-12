-- 本迁移引入异步后台 Subagent 与多任务并行：放开每用户单任务进行中限制，建立 subagents 表与执行代数，
-- 为 messages、steps、model_calls 补充 subagent_id 隔离列，并在 model_settings 中支持可选的独立 Subagent 模型。

-- +goose Up
DROP INDEX tasks_one_in_progress_per_user;

CREATE TABLE subagents (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
    status text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed', 'cancelled')),
    lease_epoch bigint NOT NULL DEFAULT 0,
    output text,
    error text,
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    CHECK ((status = 'running') = (finished_at IS NULL))
);
CREATE INDEX subagents_user_idx ON subagents (user_id, id DESC);
CREATE INDEX subagents_running_idx ON subagents (user_id, id) WHERE status = 'running';

ALTER TABLE steps ADD COLUMN subagent_id uuid REFERENCES subagents(id) ON DELETE CASCADE;
ALTER TABLE model_calls ADD COLUMN subagent_id uuid REFERENCES subagents(id) ON DELETE CASCADE;
ALTER TABLE messages
    ADD COLUMN subagent_id uuid REFERENCES subagents(id) ON DELETE CASCADE,
    ADD CONSTRAINT messages_subagent_invisible CHECK (subagent_id IS NULL OR NOT visible);

DROP INDEX messages_agent_sequence_idx;
DROP INDEX messages_summary_idx;
DROP INDEX model_calls_in_flight_idx;

CREATE INDEX messages_agent_sequence_idx ON messages (user_id, sequence) WHERE subagent_id IS NULL;
CREATE INDEX messages_subagent_sequence_idx ON messages (subagent_id, sequence) WHERE subagent_id IS NOT NULL;
CREATE INDEX messages_summary_idx ON messages (user_id, subagent_id, sequence) WHERE is_summary;
CREATE INDEX model_calls_in_flight_idx ON model_calls (user_id, subagent_id) WHERE outcome = 'in_flight';
CREATE INDEX model_calls_subagent_started_idx ON model_calls (subagent_id, started_at DESC, id DESC) WHERE subagent_id IS NOT NULL;
CREATE INDEX steps_subagent_unfinished_idx ON steps (subagent_id) WHERE subagent_id IS NOT NULL AND finished_at IS NULL;

ALTER TABLE model_settings
    ADD COLUMN subagent_model text CHECK (length(subagent_model) BETWEEN 1 AND 160),
    ADD COLUMN subagent_model_max_tokens integer CHECK (subagent_model_max_tokens > 0),
    ADD COLUMN subagent_context_length bigint CHECK (subagent_context_length > 0);

CREATE OR REPLACE VIEW main_work AS
SELECT u.id AS user_id
FROM users AS u
CROSS JOIN LATERAL (
    SELECT m.sequence, m.role, m.content, m.generating FROM messages AS m
    WHERE m.user_id = u.id AND m.subagent_id IS NULL
    ORDER BY m.sequence DESC LIMIT 1
) AS latest
WHERE latest.sequence > coalesce(u.main_failed_through_sequence, 0)
    AND (latest.generating OR latest.role <> 'assistant'
        OR (jsonb_typeof(latest.content -> 'tool_calls') = 'array' AND latest.content -> 'tool_calls' <> '[]'::jsonb));

-- +goose Down
CREATE OR REPLACE VIEW main_work AS
SELECT u.id AS user_id
FROM users AS u
CROSS JOIN LATERAL (
    SELECT m.sequence, m.role, m.content, m.generating FROM messages AS m
    WHERE m.user_id = u.id
    ORDER BY m.sequence DESC LIMIT 1
) AS latest
WHERE latest.sequence > coalesce(u.main_failed_through_sequence, 0)
    AND (latest.generating OR latest.role <> 'assistant'
        OR (jsonb_typeof(latest.content -> 'tool_calls') = 'array' AND latest.content -> 'tool_calls' <> '[]'::jsonb));

ALTER TABLE model_settings
    DROP COLUMN subagent_context_length,
    DROP COLUMN subagent_model_max_tokens,
    DROP COLUMN subagent_model;

DELETE FROM messages WHERE subagent_id IS NOT NULL;
DELETE FROM steps WHERE subagent_id IS NOT NULL;
DELETE FROM model_calls WHERE subagent_id IS NOT NULL;

DROP INDEX steps_subagent_unfinished_idx;
DROP INDEX model_calls_subagent_started_idx;
DROP INDEX model_calls_in_flight_idx;
DROP INDEX messages_summary_idx;
DROP INDEX messages_subagent_sequence_idx;
DROP INDEX messages_agent_sequence_idx;

ALTER TABLE messages DROP CONSTRAINT messages_subagent_invisible, DROP COLUMN subagent_id;
ALTER TABLE model_calls DROP COLUMN subagent_id;
ALTER TABLE steps DROP COLUMN subagent_id;
DROP TABLE subagents;

CREATE INDEX messages_agent_sequence_idx ON messages (user_id, sequence);
CREATE INDEX messages_summary_idx ON messages (user_id, sequence) WHERE is_summary;
CREATE INDEX model_calls_in_flight_idx ON model_calls (user_id) WHERE outcome = 'in_flight';

WITH extra AS (
    SELECT id FROM (
        SELECT id, row_number() OVER (PARTITION BY user_id ORDER BY updated_at DESC, id DESC) AS rn
        FROM tasks WHERE status = 'in_progress'
    ) AS ranked WHERE rn > 1
)
UPDATE tasks SET status = 'queued', updated_at = now() WHERE id IN (SELECT id FROM extra);
CREATE UNIQUE INDEX tasks_one_in_progress_per_user ON tasks(user_id) WHERE status = 'in_progress';
