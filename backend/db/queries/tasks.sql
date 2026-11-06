-- 本文件维护按用户串行的需求队列与新输入登记位置；Main 的登记工具与回执共用事务，模型选择下一项，运行时保证一次只执行一项任务。

-- name: GetNextTaskInput :one
SELECT m.id, m.sequence, m.content FROM messages AS m JOIN users AS u ON u.id = m.user_id
WHERE m.user_id = $1 AND m.sender = 'user' AND m.role = 'user' AND NOT m.is_summary
    AND m.sequence > u.task_input_through_sequence
ORDER BY m.sequence LIMIT 1;

-- name: AdvanceTaskInput :exec
UPDATE users SET task_input_through_sequence = $2 WHERE id = $1;

-- name: ListTasks :many
SELECT sqlc.embed(t), ARRAY(
    SELECT m.id FROM task_messages AS tm JOIN messages AS m ON m.id = tm.message_id
    WHERE tm.task_id = t.id ORDER BY m.sequence
)::uuid[] AS related_message_ids
FROM tasks AS t WHERE t.user_id = sqlc.arg(user_id)
    AND (sqlc.arg(include_closed)::boolean OR t.status NOT IN ('completed', 'cancelled'))
ORDER BY CASE t.status WHEN 'in_progress' THEN 0 WHEN 'queued' THEN 1 WHEN 'waiting' THEN 2 ELSE 3 END,
    t.id
LIMIT 100;

-- name: GetTask :one
SELECT sqlc.embed(t), ARRAY(
    SELECT m.id FROM task_messages AS tm JOIN messages AS m ON m.id = tm.message_id
    WHERE tm.task_id = t.id ORDER BY m.sequence
)::uuid[] AS related_message_ids
FROM tasks AS t WHERE t.user_id = $1 AND t.id = $2;

-- name: GetActiveTask :one
SELECT sqlc.embed(t), ARRAY(
    SELECT m.id FROM task_messages AS tm JOIN messages AS m ON m.id = tm.message_id
    WHERE tm.task_id = t.id ORDER BY m.sequence
)::uuid[] AS related_message_ids
FROM tasks AS t WHERE t.user_id = $1 AND t.status = 'in_progress';

-- name: CreateTask :one
INSERT INTO tasks (user_id, title, description)
VALUES ($1, $2, $3) RETURNING id;

-- name: ReviseTaskFromInput :exec
UPDATE tasks SET title = $3, description = $4, status = $5, updated_at = now()
WHERE user_id = $1 AND id = $2;

-- name: LinkTaskMessage :exec
INSERT INTO task_messages (task_id, message_id) VALUES ($1, $2)
ON CONFLICT (task_id, message_id) DO NOTHING;

-- name: HasQueuedTasks :one
SELECT EXISTS (SELECT 1 FROM tasks WHERE user_id = $1 AND status = 'queued');

-- name: UpdateTaskStatus :execrows
-- 模型选择具体任务：没有当前任务和待登记输入时才能启动待执行项，其余状态变更只针对当前任务。
UPDATE tasks AS t SET status = sqlc.arg(status)::text, updated_at = now()
WHERE t.user_id = sqlc.arg(user_id) AND t.id = sqlc.arg(id) AND (
    t.status = 'in_progress'
    OR (
        t.status = 'queued' AND sqlc.arg(status)::text = 'in_progress'
        AND NOT EXISTS (SELECT 1 FROM tasks WHERE user_id = sqlc.arg(user_id) AND status = 'in_progress')
        AND NOT EXISTS (
            SELECT 1 FROM messages AS m JOIN users AS u ON u.id = m.user_id
            WHERE m.user_id = sqlc.arg(user_id) AND m.sender = 'user' AND m.role = 'user'
                AND NOT m.is_summary AND m.sequence > u.task_input_through_sequence
        )
    )
);
