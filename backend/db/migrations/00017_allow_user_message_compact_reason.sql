-- +goose Up
ALTER TABLE model_calls DROP CONSTRAINT model_calls_compact_reason_check;
ALTER TABLE model_calls ADD CONSTRAINT model_calls_compact_reason_check CHECK (compact_reason IN ('idle', 'tokens', 'capacity', 'user_message'));

-- +goose Down
ALTER TABLE model_calls DROP CONSTRAINT model_calls_compact_reason_check;
ALTER TABLE model_calls ADD CONSTRAINT model_calls_compact_reason_check CHECK (compact_reason IN ('idle', 'tokens', 'capacity'));
