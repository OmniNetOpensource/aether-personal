-- 本迁移让 Main 是否有活只看消息尾部；任务队列不再单独唤醒或续跑，任务状态只由模型通过工具维护。

-- +goose Up
CREATE OR REPLACE VIEW main_work AS
SELECT u.id AS user_id
FROM users AS u
CROSS JOIN LATERAL (
    SELECT m.sequence, m.role, m.content, m.generating FROM messages AS m
    WHERE m.user_id = u.id
    ORDER BY m.sequence DESC LIMIT 1
) AS latest
WHERE latest.sequence > coalesce(u.main_failed_through_sequence, 0)
    AND (latest.generating OR latest.role <> 'assistant'
        OR (jsonb_typeof(latest.content -> 'tool_calls') = 'array' AND latest.content -> 'tool_calls' <> '[]'::jsonb));

-- +goose Down
CREATE OR REPLACE VIEW main_work AS
SELECT u.id AS user_id FROM users AS u
CROSS JOIN LATERAL (
    SELECT m.sequence, m.role, m.content, m.generating FROM messages AS m
    WHERE m.user_id = u.id ORDER BY m.sequence DESC LIMIT 1
) AS latest
WHERE latest.sequence > coalesce(u.main_failed_through_sequence, 0)
    AND (latest.generating OR latest.role <> 'assistant'
        OR (jsonb_typeof(latest.content -> 'tool_calls') = 'array' AND latest.content -> 'tool_calls' <> '[]'::jsonb)
        OR EXISTS (SELECT 1 FROM tasks AS t WHERE t.user_id = u.id AND t.status IN ('queued', 'in_progress'))
        OR EXISTS (SELECT 1 FROM messages AS m WHERE m.user_id = u.id AND m.sender = 'user'
            AND m.role = 'user' AND NOT m.is_summary AND m.sequence > u.task_input_through_sequence));
