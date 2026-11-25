-- 在 steps 表记录供应商返回的缓存命中输入 Token 数，便于直接统计 Prompt Cache 命中率。
-- +goose Up
ALTER TABLE steps
    ADD COLUMN cached_input_tokens integer CHECK (cached_input_tokens >= 0);

-- +goose Down
ALTER TABLE steps
    DROP COLUMN cached_input_tokens;
