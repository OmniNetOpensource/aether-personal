-- 本文件维护用户的持久化需求队列（支持多项独立任务并行执行），并提供模型任务列表与聊天端实时任务快照查询。

-- name: ListTasks :many
SELECT sqlc.embed(t)
FROM tasks AS t WHERE t.user_id = sqlc.arg(user_id)
    AND (sqlc.arg(include_closed)::boolean OR t.status NOT IN ('completed', 'cancelled'))
ORDER BY CASE t.status WHEN 'in_progress' THEN 0 WHEN 'queued' THEN 1 WHEN 'waiting' THEN 2 ELSE 3 END,
    t.id
LIMIT 100;

-- name: ListChatTasks :many
-- 返回聊天端实时展示的任务快照：包含全部未关闭任务，以及最近结束的 30 项历史任务。
SELECT snapshot.id, snapshot.title, snapshot.description, snapshot.status, snapshot.created_at, snapshot.updated_at
FROM (
    SELECT t.id, t.title, t.description, t.status, t.created_at, t.updated_at,
        CASE t.status WHEN 'in_progress' THEN 0 WHEN 'queued' THEN 1 ELSE 2 END AS group_order
    FROM tasks AS t
    WHERE t.user_id = $1 AND t.status IN ('in_progress', 'queued', 'waiting')
    UNION ALL
    (
        SELECT t.id, t.title, t.description, t.status, t.created_at, t.updated_at,
            3 AS group_order
        FROM tasks AS t
        WHERE t.user_id = $1 AND t.status IN ('completed', 'cancelled')
        ORDER BY t.updated_at DESC, t.id DESC
        LIMIT 30
    )
) AS snapshot
ORDER BY snapshot.group_order,
    CASE WHEN snapshot.group_order < 3 THEN snapshot.id END ASC,
    snapshot.updated_at DESC,
    snapshot.id DESC;

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
-- 排队中的任务可启动为 in_progress（支持多项并行），完成、等待或退回排队只针对正在进行中的任务。
UPDATE tasks AS t SET status = sqlc.arg(status)::text, updated_at = now()
WHERE t.user_id = sqlc.arg(user_id) AND t.id = sqlc.arg(id) AND (
    t.status = 'in_progress'
    OR (t.status = 'queued' AND sqlc.arg(status)::text = 'in_progress')
);
