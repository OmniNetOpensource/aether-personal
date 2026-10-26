-- +goose Up
ALTER TABLE model_settings
    ADD COLUMN main_model_max_tokens integer CHECK (main_model_max_tokens > 0),
    ADD COLUMN subagent_model_max_tokens integer CHECK (subagent_model_max_tokens > 0);

-- +goose Down
ALTER TABLE model_settings DROP COLUMN subagent_model_max_tokens, DROP COLUMN main_model_max_tokens;
