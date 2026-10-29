-- +goose Up
DROP TABLE sandbox_operations;
DROP TABLE user_sandboxes;
ALTER TABLE steps DROP COLUMN resume_after;

-- +goose Down
ALTER TABLE steps ADD COLUMN resume_after timestamptz;

CREATE TABLE user_sandboxes (
    user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    allocation_id uuid NOT NULL UNIQUE DEFAULT uuidv7(),
    provider_id text UNIQUE,
    provision_state text NOT NULL DEFAULT 'pending'
        CHECK (provision_state IN ('pending', 'creating', 'ready', 'unknown', 'missing')),
    last_activity_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sandbox_operations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    step_id uuid NOT NULL REFERENCES steps (id) ON DELETE CASCADE,
    tool_call_id text NOT NULL,
    tool_name text NOT NULL,
    args jsonb NOT NULL,
    state text NOT NULL DEFAULT 'planned'
        CHECK (state IN ('planned', 'submitting', 'running', 'completed', 'unknown')),
    provider_command_id text,
    result jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (step_id, tool_call_id)
);

CREATE INDEX sandbox_operations_user_state_idx ON sandbox_operations (user_id, state);
