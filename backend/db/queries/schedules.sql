-- 本文件是定时安排的查询集合：创建与维护 schedule、扫描到期安排并在投递后推进下一次触发（sqlc 据此生成代码）。

-- name: CreateSchedule :one
INSERT INTO schedules (user_id, title, context, run_at, cron, timezone, status, next_run_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListSchedules :many
SELECT * FROM schedules
WHERE user_id = sqlc.arg(user_id)
    AND (sqlc.narg(before_id)::uuid IS NULL OR id < sqlc.narg(before_id)::uuid)
ORDER BY id DESC LIMIT 51;

-- name: LockSchedule :one
SELECT * FROM schedules WHERE user_id = $1 AND id = $2 FOR UPDATE;

-- name: UpdateSchedule :one
UPDATE schedules SET title = $3, context = $4, run_at = $5, cron = $6,
    timezone = $7, status = $8, next_run_at = $9, version = version + 1, updated_at = now()
WHERE user_id = $1 AND id = $2
RETURNING *;

-- name: DeleteSchedule :execrows
DELETE FROM schedules WHERE user_id = $1 AND id = $2;

-- name: ListDueSchedules :many
SELECT id, user_id FROM schedules
WHERE status = 'active' AND next_run_at <= now()
ORDER BY next_run_at, id LIMIT 100;

-- name: LockDueSchedule :one
SELECT schedules.*, clock_timestamp()::timestamptz AS delivery_time
FROM schedules WHERE user_id = $1 AND id = $2
    AND status = 'active' AND next_run_at <= now()
FOR UPDATE SKIP LOCKED;

-- name: DeliverScheduleMessage :one
INSERT INTO messages (user_id, sequence, role, content, sender)
VALUES ($1, NULL, 'user', $2, 'system') RETURNING id;

-- name: InsertScheduleOccurrence :exec
INSERT INTO schedule_occurrences (id, schedule_id, schedule_version, scheduled_for, delivered_at, context, trigger_message_id)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: AdvanceSchedule :exec
UPDATE schedules SET status = $3, next_run_at = $4, updated_at = now()
WHERE user_id = $1 AND id = $2;
