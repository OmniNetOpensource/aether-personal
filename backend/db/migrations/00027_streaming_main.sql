-- +goose Up
-- 生成中的回复先占一个对话位置，完整返回后才对聊天可见；进程崩溃时它仍是可恢复的工作。
ALTER TABLE messages ADD COLUMN generating boolean NOT NULL DEFAULT false;
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

-- 用户与日程共用输入通知，仅实际插入才触发。事务提交后，持有 Main 的实例才能收到。
-- +goose StatementBegin
CREATE FUNCTION notify_main_input() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('main_input', json_build_object('user_id', NEW.user_id, 'sequence', NEW.sequence)::text);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER main_input_insert AFTER INSERT ON messages
FOR EACH ROW WHEN (NEW.role = 'user' AND NOT NEW.is_summary) EXECUTE FUNCTION notify_main_input();

-- +goose Down
DROP TRIGGER main_input_insert ON messages;
DROP FUNCTION notify_main_input();
DELETE FROM messages WHERE generating;
CREATE OR REPLACE VIEW main_work AS
SELECT u.id AS user_id
FROM users AS u
CROSS JOIN LATERAL (
    SELECT m.sequence, m.role, m.content FROM messages AS m
    WHERE m.user_id = u.id
    ORDER BY m.sequence DESC LIMIT 1
) AS latest
WHERE latest.sequence > coalesce(u.main_failed_through_sequence, 0)
    AND (latest.role <> 'assistant'
        OR (jsonb_typeof(latest.content -> 'tool_calls') = 'array' AND latest.content -> 'tool_calls' <> '[]'::jsonb));
ALTER TABLE messages DROP COLUMN generating;
