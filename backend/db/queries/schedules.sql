-- 本文件是定时触发的查询集合：schedule 工具的增删改查、调度器扫描到期安排并推进下一次时间，以及闲置后台工作的到期判断与触发消息写入（sqlc 据此生成代码）。

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

-- name: ListIdleWorkDue :many
-- 一种闲置后台工作的到期条件：用户最近一条消息之后这种工作还没投过、前一种工作已经投过（previous_work_name 为空表示没有前一种）、
-- Main 没有活、用户最近一条消息已过 idle_minutes。从用户消息起算，所以前一种工作自己产生的消息不会推迟后一种。
-- 最近一条用户消息早于闲置时长再加一天的不算，上线或停机恢复后不给很久以前的对话补跑。user_id 为空时扫描全部用户，投递事务内按用户复查。
SELECT u.id AS user_id FROM users AS u
CROSS JOIN LATERAL (
    SELECT m.sequence, m.created_at FROM messages AS m
    WHERE m.user_id = u.id AND m.sender = 'user' AND m.role = 'user' AND NOT m.is_summary
    ORDER BY m.sequence DESC LIMIT 1
) AS last_input
WHERE (sqlc.narg(user_id)::uuid IS NULL OR u.id = sqlc.narg(user_id)::uuid)
    AND last_input.created_at <= now() - make_interval(mins => sqlc.arg(idle_minutes)::int)
    AND last_input.created_at > now() - make_interval(mins => sqlc.arg(idle_minutes)::int) - interval '1 day'
    AND NOT EXISTS (SELECT 1 FROM main_work AS w WHERE w.user_id = u.id)
    AND NOT EXISTS (SELECT 1 FROM messages AS m WHERE m.user_id = u.id
        AND m.sequence > last_input.sequence AND m.content ->> 'name' = sqlc.arg(work_name)::text)
    AND (sqlc.arg(previous_work_name)::text = '' OR EXISTS (SELECT 1 FROM messages AS m WHERE m.user_id = u.id
        AND m.sequence > last_input.sequence AND m.content ->> 'name' = sqlc.arg(previous_work_name)::text));

-- name: InsertTriggerMessage :one
-- 定时投递与闲置后台工作共用：不可见的 user 消息，由系统发出，入库即成为 Main 的新输入。
INSERT INTO messages (user_id, role, content, sender, visible)
VALUES ($1, 'user', $2, 'system', false)
RETURNING sequence;
