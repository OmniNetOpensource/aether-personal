-- 本迁移移除持久化需求队列 tasks 表及对应索引；历史消息表内的旧工具记录保留不变。

-- +goose Up
DROP TABLE tasks;

-- +goose Down
CREATE TABLE tasks (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
    description text NOT NULL CHECK (length(btrim(description)) BETWEEN 1 AND 32000),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'in_progress', 'waiting', 'completed', 'cancelled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tasks_queue_idx ON tasks(user_id, id) WHERE status = 'queued';
CREATE INDEX tasks_user_status_idx ON tasks(user_id, status, id);
