-- 本迁移删除任务重复保存的来源序号，保留按 UUIDv7 ID 列举的索引；下一项任务由模型选择。

-- +goose Up
DROP INDEX tasks_queue_idx;
ALTER TABLE tasks DROP COLUMN source_sequence;
CREATE INDEX tasks_queue_idx ON tasks(user_id, id) WHERE status = 'queued';

-- +goose Down
-- 降级按仍保留的关联用户输入重建来源序号；没有关联输入时使用 0，无法还原已删除消息的原始序号。
DROP INDEX tasks_queue_idx;
ALTER TABLE tasks ADD COLUMN source_sequence bigint;
UPDATE tasks AS t SET source_sequence = coalesce((
    SELECT min(m.sequence) FROM task_messages AS tm JOIN messages AS m ON m.id = tm.message_id
    WHERE tm.task_id = t.id AND m.role = 'user' AND m.sender = 'user'
), 0);
ALTER TABLE tasks ALTER COLUMN source_sequence SET NOT NULL;
CREATE INDEX tasks_queue_idx ON tasks(user_id, source_sequence, id) WHERE status = 'queued';
