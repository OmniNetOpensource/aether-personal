-- 本文件是定时安排的查询集合：schedule 工具的增删改查，以及调度器扫描到期安排、投递触发消息并推进下一次时间（sqlc 据此生成代码）。

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

-- name: InsertTriggerMessage :one
-- 不可见的 user 消息，由系统发出，入库即成为 Main 的新输入。
INSERT INTO messages (user_id, role, content, sender, visible)
VALUES ($1, 'user', $2, 'system', false)
RETURNING sequence;
