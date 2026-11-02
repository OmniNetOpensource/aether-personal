-- +goose Up
-- 去掉消息的待读与已读状态：消息一入库就占序号，Main 要不要运行只看消息表尾部。
-- 先按到达顺序给残留的待读消息补号，再把 sequence 设为必填。
UPDATE messages AS m SET sequence = pending.next_sequence
FROM (
    SELECT ordered.id, nextval('message_sequence') AS next_sequence
    FROM (SELECT id FROM messages WHERE sequence IS NULL ORDER BY id) AS ordered
) AS pending
WHERE m.id = pending.id;
ALTER TABLE messages ALTER COLUMN sequence SET NOT NULL;

-- 指令、当前时间与进度提醒改为组装请求时现算，不再落库；旧行删除，避免与现算的内容重复。
DELETE FROM messages WHERE content ->> 'name' IN ('instructions', 'current_time', 'progress_reminder');
DROP INDEX messages_instructions_idx;

-- 模型回复与 assistant 消息在同一事务落库，不再先存进 steps 等待恢复。
ALTER TABLE steps DROP COLUMN model_reply;

-- Main 运行失败时记下当时最后一条消息的序号；在出现更新的消息之前不再自动运行，避免同一失败反复重试。
ALTER TABLE users ADD COLUMN main_failed_through_sequence bigint;

-- main_work 列出现在需要 Main 运行的用户：最后一条消息是 user 或 tool，或是带工具调用的 assistant，且不在失败后的等待中。
-- 运行调度、聊天的思考状态与看板共用这一个定义。
CREATE VIEW main_work AS
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

-- +goose Down
-- 只恢复结构，已删除的运行时注入行不恢复。
DROP VIEW main_work;
ALTER TABLE users DROP COLUMN main_failed_through_sequence;
ALTER TABLE steps ADD COLUMN model_reply jsonb;
CREATE INDEX messages_instructions_idx ON messages (user_id, id) WHERE content ->> 'name' = 'instructions';
ALTER TABLE messages ALTER COLUMN sequence DROP NOT NULL;
