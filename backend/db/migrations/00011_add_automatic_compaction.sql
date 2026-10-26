-- +goose Up
ALTER TABLE model_settings
    ADD COLUMN compact_after_hours integer NOT NULL DEFAULT 6 CHECK (compact_after_hours BETWEEN 1 AND 8760),
    ADD COLUMN compact_at_tokens integer NOT NULL DEFAULT 200000 CHECK (compact_at_tokens BETWEEN 1000 AND 2000000),
    ADD COLUMN main_context_length bigint CHECK (main_context_length > 0),
    ADD COLUMN subagent_context_length bigint CHECK (subagent_context_length > 0);
ALTER TABLE messages
    ADD COLUMN summary_through_sequence bigint,
    ADD COLUMN compact_reply_sequence bigint,
    ADD CONSTRAINT messages_summary_boundary CHECK (is_summary = (summary_through_sequence IS NOT NULL)),
    ADD CONSTRAINT messages_summary_order CHECK (summary_through_sequence < sequence),
    ADD CONSTRAINT messages_compact_reply CHECK (compact_reply_sequence IS NULL OR is_summary);
ALTER TABLE steps ADD COLUMN purpose text NOT NULL DEFAULT 'agent' CHECK (purpose IN ('agent', 'compact'));
ALTER TABLE model_calls
    ADD COLUMN purpose text NOT NULL DEFAULT 'agent' CHECK (purpose IN ('agent', 'compact')),
    ADD COLUMN compact_reason text CHECK (compact_reason IN ('idle', 'tokens', 'capacity')),
    ADD COLUMN context_tokens_before integer CHECK (context_tokens_before >= 0),
    ADD COLUMN context_tokens_after integer CHECK (context_tokens_after >= 0),
    ADD COLUMN summary_through_sequence bigint;

-- +goose Down
ALTER TABLE model_calls DROP COLUMN summary_through_sequence, DROP COLUMN context_tokens_after,
    DROP COLUMN context_tokens_before, DROP COLUMN compact_reason, DROP COLUMN purpose;
ALTER TABLE steps DROP COLUMN purpose;
ALTER TABLE messages DROP CONSTRAINT messages_compact_reply, DROP CONSTRAINT messages_summary_order,
    DROP CONSTRAINT messages_summary_boundary, DROP COLUMN compact_reply_sequence, DROP COLUMN summary_through_sequence;
ALTER TABLE model_settings DROP COLUMN subagent_context_length, DROP COLUMN main_context_length,
    DROP COLUMN compact_at_tokens, DROP COLUMN compact_after_hours;
