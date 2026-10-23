-- +goose Up
ALTER TABLE subagents ADD COLUMN started_at timestamptz;

-- +goose Down
ALTER TABLE subagents DROP COLUMN started_at;
