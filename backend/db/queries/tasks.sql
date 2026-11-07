-- 本文件维护按用户串行的需求队列；任务只保存自身的要求与状态，模型选择下一项，运行时保证一次只执行一项任务。

-- name: ListTasks :many
SELECT sqlc.embed(t)
FROM tasks AS t WHERE t.user_id = sqlc.arg(user_id)
    AND (sqlc.arg(include_closed)::boolean OR t.status NOT IN ('completed', 'cancelled'))
ORDER BY CASE t.status WHEN 'in_progress' THEN 0 WHEN 'queued' THEN 1 WHEN 'waiting' THEN 2 ELSE 3 END,
    t.id
LIMIT 100;

-- name: GetTask :one
SELECT sqlc.embed(t)
FROM tasks AS t WHERE t.user_id = $1 AND t.id = $2;

-- name: CreateTask :one
INSERT INTO tasks (user_id, title, description)
VALUES ($1, $2, $3) RETURNING id;

-- name: ReviseTaskFromInput :exec
UPDATE tasks SET title = $3, description = $4, status = $5, updated_at = now()
WHERE user_id = $1 AND id = $2;

-- name: UpdateTaskStatus :execrows
-- 模型选择具体任务：没有当前任务时才能启动待执行项，其余状态变更只针对当前任务。
UPDATE tasks AS t SET status = sqlc.arg(status)::text, updated_at = now()
WHERE t.user_id = sqlc.arg(user_id) AND t.id = sqlc.arg(id) AND (
    t.status = 'in_progress'
    OR (
        t.status = 'queued' AND sqlc.arg(status)::text = 'in_progress'
        AND NOT EXISTS (SELECT 1 FROM tasks WHERE user_id = sqlc.arg(user_id) AND status = 'in_progress')
    )
);
