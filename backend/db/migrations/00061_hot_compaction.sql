-- 热压缩在 Main 空闲时生成摘要；这条内部消息不单独唤醒对话。
-- +goose Up
ALTER TABLE model_settings ADD COLUMN hot_compact_delay_seconds integer NOT NULL DEFAULT 159 CHECK (hot_compact_delay_seconds > 0);
ALTER TABLE steps DROP CONSTRAINT steps_purpose_check;
ALTER TABLE steps ADD CONSTRAINT steps_purpose_check CHECK (purpose IN ('agent', 'compact', 'hot_compact'));

CREATE OR REPLACE VIEW main_work AS
SELECT u.id AS user_id
FROM users AS u
CROSS JOIN LATERAL (
    SELECT m.sequence, m.role, m.content, m.generating FROM messages AS m
    WHERE m.user_id = u.id AND m.subagent_id IS NULL
      AND NOT EXISTS (SELECT 1 FROM steps AS s WHERE m.is_summary AND s.id = m.step_id AND s.purpose = 'hot_compact')
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
    WHERE m.user_id = u.id AND m.subagent_id IS NULL
    ORDER BY m.sequence DESC LIMIT 1
) AS latest
WHERE latest.sequence > coalesce(u.main_failed_through_sequence, 0)
    AND (latest.generating OR latest.role <> 'assistant'
        OR (jsonb_typeof(latest.content -> 'tool_calls') = 'array' AND latest.content -> 'tool_calls' <> '[]'::jsonb));
ALTER TABLE steps DROP CONSTRAINT steps_purpose_check;
UPDATE steps SET purpose = 'compact' WHERE purpose = 'hot_compact';
ALTER TABLE steps ADD CONSTRAINT steps_purpose_check CHECK (purpose IN ('agent', 'compact'));
ALTER TABLE model_settings DROP COLUMN hot_compact_delay_seconds;
