-- 本迁移建立用户需求队列与输入登记位置，并让 Main 在当前任务结束后继续领取下一项。

-- +goose Up
-- 任务独立于聊天持久化；输入登记位置只标记已分类的用户消息，不代表任务完成。
ALTER TABLE users ADD COLUMN task_input_through_sequence bigint NOT NULL DEFAULT 0;
-- 不重做已结束的历史对话；最后一次完整收尾之后仍未处理的输入进入任务登记。
UPDATE users AS u SET task_input_through_sequence = coalesce((
    SELECT max(m.sequence) FROM messages AS m WHERE m.user_id = u.id
        AND m.role = 'assistant' AND NOT m.generating
        AND coalesce(m.content -> 'tool_calls', '[]'::jsonb) IN ('[]'::jsonb, 'null'::jsonb)
), 0);

CREATE TABLE tasks (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
    description text NOT NULL CHECK (length(btrim(description)) BETWEEN 1 AND 32000),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'in_progress', 'waiting', 'completed', 'cancelled')),
    source_sequence bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- 每条需求保存全部关联用户消息，补充时追加；关联用外键维护，删除消息或任务时同步清除。
CREATE TABLE task_messages (
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, message_id)
);
CREATE INDEX task_messages_message_idx ON task_messages(message_id);

CREATE UNIQUE INDEX tasks_one_in_progress_per_user ON tasks(user_id) WHERE status = 'in_progress';
CREATE INDEX tasks_queue_idx ON tasks(user_id, source_sequence, id) WHERE status = 'queued';
CREATE INDEX tasks_user_status_idx ON tasks(user_id, status, id);
CREATE INDEX messages_task_input_idx ON messages(user_id, sequence) WHERE sender = 'user' AND role = 'user' AND NOT is_summary;

-- 聊天尾部结束不再等于全部需求结束：待登记输入与可执行任务也会唤醒同一个 Main。
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

-- +goose Down
CREATE OR REPLACE VIEW main_work AS
SELECT u.id AS user_id FROM users AS u
CROSS JOIN LATERAL (
    SELECT m.sequence, m.role, m.content, m.generating FROM messages AS m
    WHERE m.user_id = u.id ORDER BY m.sequence DESC LIMIT 1
) AS latest
WHERE latest.sequence > coalesce(u.main_failed_through_sequence, 0)
    AND (latest.generating OR latest.role <> 'assistant'
        OR (jsonb_typeof(latest.content -> 'tool_calls') = 'array' AND latest.content -> 'tool_calls' <> '[]'::jsonb));
DROP INDEX messages_task_input_idx;
DROP TABLE task_messages;
DROP TABLE tasks;
ALTER TABLE users DROP COLUMN task_input_through_sequence;
