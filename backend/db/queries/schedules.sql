-- 本文件是定时触发的查询集合：schedule 工具的增删改查、调度器扫描到期安排并推进下一次时间，以及闲置后台反思的到期判断与触发消息写入（sqlc 据此生成代码）。

-- name: CreateSchedule :one
INSERT INTO schedules (user_id, title, context, cron, timezone, next_run_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListSchedules :many
SELECT * FROM schedules WHERE user_id = $1 ORDER BY next_run_at, id;

-- name: LockSchedule :one
SELECT * FROM schedules WHERE user_id = $1 AND id = $2 FOR UPDATE;

-- name: UpdateSchedule :one
UPDATE schedules SET title = $3, context = $4, cron = $5, timezone = $6, next_run_at = $7, updated_at = now()
WHERE user_id = $1 AND id = $2
RETURNING *;

-- name: DeleteSchedule :execrows
DELETE FROM schedules WHERE user_id = $1 AND id = $2;

-- name: ListDueSchedules :many
SELECT id, user_id FROM schedules WHERE next_run_at <= now() ORDER BY next_run_at, id LIMIT 100;

-- name: LockDueSchedule :one
-- 投递事务先取用户消息锁再锁这一行，与工具修改安排的加锁顺序一致；扫描后已被修改到未来或删除的安排不再返回。
SELECT * FROM schedules WHERE id = $1 AND next_run_at <= now() FOR UPDATE SKIP LOCKED;

-- name: AdvanceSchedule :exec
UPDATE schedules SET next_run_at = $2, updated_at = now() WHERE id = $1;

-- name: ListReflectionDue :many
-- 闲置后台反思的到期条件：该用户发过本人消息、Main 与后台 Subagent 当前都没有活，
-- 且 Main 最新一条消息入库时间距今已满 idle_minutes。
-- 若最近一条主对话输入本身是一次空的 proactive（reflection 或历史同类触发，且其后没有任何对用户可见的消息），
-- 则不再继续触发，直到出现新的用户输入、定时或 Subagent 事件为止；user_id 为空时扫描全部用户，投递事务内按用户复查。
SELECT u.id AS user_id FROM users AS u
WHERE (sqlc.narg(user_id)::uuid IS NULL OR u.id = sqlc.narg(user_id)::uuid)
    AND EXISTS (
        SELECT 1 FROM messages AS m
        WHERE m.user_id = u.id AND m.sender = 'user' AND m.role = 'user' AND NOT m.is_summary
    )
    AND NOT EXISTS (SELECT 1 FROM main_work AS w WHERE w.user_id = u.id)
    AND NOT EXISTS (SELECT 1 FROM subagents AS sa WHERE sa.user_id = u.id AND sa.status = 'running')
    AND (
        SELECT m.created_at FROM messages AS m
        WHERE m.user_id = u.id AND m.subagent_id IS NULL
        ORDER BY m.sequence DESC LIMIT 1
    ) <= now() - make_interval(mins => sqlc.arg(idle_minutes)::int)
    AND NOT EXISTS (
        SELECT 1
        FROM (
            SELECT m.sequence, m.content
            FROM messages AS m
            WHERE m.user_id = u.id
                AND m.subagent_id IS NULL
                AND m.role = 'user'
                AND NOT m.is_summary
                AND m.content ->> 'name' IS DISTINCT FROM 'memory_context' AND m.content ->> 'name' IS DISTINCT FROM 'compact_reminder'
            ORDER BY m.sequence DESC
            LIMIT 1
        ) AS last_input
        WHERE last_input.content ->> 'name' IN ('reflection', 'memory_update', 'recent_followup', 'unknown_unknowns')
            AND NOT EXISTS (
                SELECT 1
                FROM messages AS v
                WHERE v.user_id = u.id
                    AND v.subagent_id IS NULL
                    AND v.sequence > last_input.sequence
                    AND v.visible
            )
    );

-- name: InsertTriggerMessage :one
-- 定时投递与闲置后台反思共用：不可见的 user 消息，由系统发出，入库即成为 Main 的新输入。
INSERT INTO messages (user_id, role, content, sender, visible)
VALUES ($1, 'user', $2, 'system', false)
RETURNING sequence;
