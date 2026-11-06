-- 本迁移为每条助手回复保存唯一任务归属；任务可以有多条回复，完成状态仍独立于回复数量和未来文件交付。

-- +goose Up
ALTER TABLE messages ADD COLUMN task_id uuid REFERENCES tasks(id) ON DELETE CASCADE;
ALTER TABLE messages ADD CONSTRAINT messages_task_only_assistant CHECK (task_id IS NULL OR role = 'assistant');
-- 旧历史无法可靠推断任务，不回填；NOT VALID 保留旧行，但约束迁移后新增和更新的用户可见回复。
ALTER TABLE messages ADD CONSTRAINT messages_visible_assistant_has_task
    CHECK (role <> 'assistant' OR NOT visible OR task_id IS NOT NULL) NOT VALID;
CREATE INDEX messages_task_sequence_idx ON messages(task_id, sequence) WHERE task_id IS NOT NULL;

-- +goose Down
DROP INDEX messages_task_sequence_idx;
ALTER TABLE messages DROP CONSTRAINT messages_visible_assistant_has_task;
ALTER TABLE messages DROP CONSTRAINT messages_task_only_assistant;
ALTER TABLE messages DROP COLUMN task_id;
