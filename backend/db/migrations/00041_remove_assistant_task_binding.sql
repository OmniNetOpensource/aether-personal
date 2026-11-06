-- 本迁移移除助手回答的任务归属；任务只管理需求与状态，保留相关用户输入，不绑定消息或文件产物。

-- +goose Up
DROP INDEX messages_task_sequence_idx;
ALTER TABLE messages DROP CONSTRAINT messages_visible_assistant_has_task;
ALTER TABLE messages DROP CONSTRAINT messages_task_only_assistant;
ALTER TABLE messages DROP COLUMN task_id;

-- +goose Down
-- 恢复旧结构，不推断已移除的回答归属；NOT VALID 保留无任务的历史回答。
ALTER TABLE messages ADD COLUMN task_id uuid REFERENCES tasks(id) ON DELETE CASCADE;
ALTER TABLE messages ADD CONSTRAINT messages_task_only_assistant CHECK (task_id IS NULL OR role = 'assistant');
ALTER TABLE messages ADD CONSTRAINT messages_visible_assistant_has_task
    CHECK (role <> 'assistant' OR NOT visible OR task_id IS NOT NULL) NOT VALID;
CREATE INDEX messages_task_sequence_idx ON messages(task_id, sequence) WHERE task_id IS NOT NULL;
