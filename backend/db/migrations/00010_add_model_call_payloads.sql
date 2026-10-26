-- +goose Up
ALTER TABLE model_calls ADD COLUMN request_body jsonb
    CHECK (request_body IS NULL OR jsonb_typeof(request_body) = 'object');
ALTER TABLE model_calls ADD COLUMN response_body bytea;
ALTER TABLE model_calls ADD COLUMN response_truncated boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE model_calls DROP COLUMN request_body;
ALTER TABLE model_calls DROP COLUMN response_body;
ALTER TABLE model_calls DROP COLUMN response_truncated;
