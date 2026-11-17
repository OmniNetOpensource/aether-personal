-- 本迁移移除 model_calls 表及其索引，并为 steps 补充按 Subagent 查询最近步骤的索引。

-- +goose Up
DROP TABLE model_calls;
CREATE INDEX steps_subagent_started_idx ON steps (subagent_id, started_at DESC, id DESC) WHERE subagent_id IS NOT NULL;

-- +goose Down
DROP INDEX steps_subagent_started_idx;

CREATE TABLE model_calls (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    step_id uuid NOT NULL REFERENCES steps (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    subagent_id uuid REFERENCES subagents (id) ON DELETE CASCADE,
    requested_model text NOT NULL,
    response_model text,
    provider_generation_id text,
    outcome text NOT NULL DEFAULT 'in_flight'
        CHECK (outcome IN ('in_flight', 'succeeded', 'http_error', 'transport_error', 'invalid_response', 'interrupted')),
    http_status integer CHECK (http_status BETWEEN 100 AND 599),
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    duration_ms bigint CHECK (duration_ms >= 0),
    input_tokens integer CHECK (input_tokens >= 0),
    output_tokens integer CHECK (output_tokens >= 0),
    cost numeric CHECK (cost >= 0),
    tool_names text[] NOT NULL DEFAULT '{}',
    failure_kind text CHECK (failure_kind IN ('http_error', 'transport_error', 'invalid_response', 'interrupted')),
    request_body jsonb CHECK (request_body IS NULL OR jsonb_typeof(request_body) = 'object'),
    response_body bytea,
    response_truncated boolean NOT NULL DEFAULT false,
    purpose text NOT NULL DEFAULT 'agent' CHECK (purpose IN ('agent', 'compact')),
    compact_reason text CHECK (compact_reason IN ('idle', 'tokens', 'capacity', 'user_message')),
    context_tokens_before integer CHECK (context_tokens_before >= 0),
    context_tokens_after integer CHECK (context_tokens_after >= 0),
    summary_through_sequence bigint,
    CHECK ((outcome = 'in_flight') = (finished_at IS NULL))
);
CREATE INDEX model_calls_started_idx ON model_calls (started_at DESC, id DESC);
CREATE INDEX model_calls_user_started_idx ON model_calls (user_id, started_at DESC, id DESC);
CREATE INDEX model_calls_subagent_started_idx ON model_calls (subagent_id, started_at DESC, id DESC) WHERE subagent_id IS NOT NULL;
CREATE INDEX model_calls_step_idx ON model_calls (step_id);
CREATE INDEX model_calls_in_flight_idx ON model_calls (user_id, subagent_id) WHERE outcome = 'in_flight';
