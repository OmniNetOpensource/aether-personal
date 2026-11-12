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
-- 闲置后台反思的到期条件：该用户发过本人消息、Main 当前没有活也没有进行中的模型请求，
-- 且最近一次已结束的模型请求（与最新一条消息入库时间取较晚者）距今已满 idle_minutes。
-- 以前投递过不阻止再次触发；user_id 为空时扫描全部用户，投递事务内按用户复查。
SELECT u.id AS user_id FROM users AS u
WHERE (sqlc.narg(user_id)::uuid IS NULL OR u.id = sqlc.narg(user_id)::uuid)
    AND EXISTS (
        SELECT 1 FROM messages AS m
        WHERE m.user_id = u.id AND m.sender = 'user' AND m.role = 'user' AND NOT m.is_summary
    )
    AND NOT EXISTS (SELECT 1 FROM main_work AS w WHERE w.user_id = u.id)
    AND NOT EXISTS (SELECT 1 FROM subagents AS sa WHERE sa.user_id = u.id AND sa.status = 'running')
    AND NOT EXISTS (SELECT 1 FROM model_calls AS mc WHERE mc.user_id = u.id AND mc.outcome = 'in_flight')
    AND GREATEST(
        (SELECT mc.finished_at FROM model_calls AS mc
         WHERE mc.user_id = u.id AND mc.finished_at IS NOT NULL
         ORDER BY mc.started_at DESC LIMIT 1),
        (SELECT m.created_at FROM messages AS m
         WHERE m.user_id = u.id AND m.subagent_id IS NULL
         ORDER BY m.sequence DESC LIMIT 1)
    ) <= now() - make_interval(mins => sqlc.arg(idle_minutes)::int);

-- name: InsertTriggerMessage :one
-- 定时投递与闲置后台反思共用：不可见的 user 消息，由系统发出，入库即成为 Main 的新输入。
INSERT INTO messages (user_id, role, content, sender, visible)
VALUES ($1, 'user', $2, 'system', false)
RETURNING sequence;
