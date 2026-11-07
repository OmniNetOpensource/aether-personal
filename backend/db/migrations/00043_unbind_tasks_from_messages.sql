-- 本迁移删除任务与消息的全部关联；任务只保存自身的要求与状态，不再记录来源消息或登记进度。

-- +goose Up
DROP TABLE task_messages;
ALTER TABLE users DROP COLUMN task_input_through_sequence;

-- +goose Down
-- 恢复旧结构；已删除的关联与登记进度无法还原，登记位置从 0 开始。
ALTER TABLE users ADD COLUMN task_input_through_sequence bigint NOT NULL DEFAULT 0;
CREATE TABLE task_messages (
    task_id uuid NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, message_id)
);
CREATE INDEX task_messages_message_idx ON task_messages(message_id);
