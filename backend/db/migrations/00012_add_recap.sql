-- +goose Up
ALTER TABLE steps ADD COLUMN activity jsonb NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(activity) = 'array');

ALTER TABLE model_calls ALTER COLUMN step_id DROP NOT NULL;
ALTER TABLE model_calls DROP CONSTRAINT model_calls_purpose_check;
ALTER TABLE model_calls ADD CONSTRAINT model_calls_purpose_check CHECK (purpose IN ('agent', 'compact', 'recap'));
ALTER TABLE model_calls ADD CONSTRAINT model_calls_purpose_step_check
    CHECK ((purpose IN ('agent', 'compact') AND step_id IS NOT NULL)
        OR (purpose = 'recap' AND step_id IS NULL AND subagent_id IS NULL));

CREATE VIEW active_agent_users AS
SELECT user_id FROM messages WHERE sequence IS NULL
UNION SELECT user_id FROM steps WHERE finished_at IS NULL
UNION SELECT user_id FROM subagents WHERE status = 'running';

CREATE TABLE recap_state (
    user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    active boolean NOT NULL DEFAULT true,
    work_version bigint NOT NULL DEFAULT 1,
    window_started_at timestamptz NOT NULL DEFAULT now(),
    request_total bigint NOT NULL DEFAULT 0,
    recap_request_total bigint NOT NULL DEFAULT 0,
    summary text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (request_total >= recap_request_total AND recap_request_total >= 0)
);

-- +goose Down
DROP TABLE recap_state;
DROP VIEW active_agent_users;
DELETE FROM model_calls WHERE purpose = 'recap';
ALTER TABLE model_calls DROP CONSTRAINT model_calls_purpose_step_check;
ALTER TABLE model_calls DROP CONSTRAINT model_calls_purpose_check;
ALTER TABLE model_calls ADD CONSTRAINT model_calls_purpose_check CHECK (purpose IN ('agent', 'compact'));
ALTER TABLE model_calls ALTER COLUMN step_id SET NOT NULL;
ALTER TABLE steps DROP COLUMN activity;
