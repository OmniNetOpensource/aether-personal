-- 本文件提供后台 Subagent 的创建、细粒度事务锁、执行代数抢占、运行状态流转与列表查询。

-- name: LockSubagent :exec
-- 与用户消息锁分离：Subagent 常规步骤只锁自己，需同时拿两把锁时必须先拿 LockUserMessages 再拿本锁。
SELECT pg_advisory_xact_lock(hashtextextended('subagent:' || sqlc.arg(subagent_id)::uuid::text, 0));

-- name: CreateSubagent :one
INSERT INTO subagents (user_id, title)
VALUES ($1, $2)
RETURNING id, started_at;

-- name: GetSubagentForUser :one
SELECT * FROM subagents WHERE user_id = $1 AND id = $2;

-- name: ListUserSubagents :many
SELECT id, user_id, title, status, error, started_at, finished_at
FROM subagents
WHERE user_id = $1
ORDER BY id DESC
LIMIT 100;

-- name: HasRunningSubagents :one
SELECT EXISTS (SELECT 1 FROM subagents WHERE user_id = $1 AND status = 'running');

-- name: ClaimSubagentLeaseEpoch :one
-- 实例抢到 Subagent 临时租约后原子递增代数，使此前任何超时残留实例的写库事务失效。
UPDATE subagents
SET lease_epoch = lease_epoch + 1
WHERE id = sqlc.arg(subagent_id)::uuid AND status = 'running'
RETURNING user_id, lease_epoch, started_at;

-- name: GetSubagentLeaseState :one
SELECT status, lease_epoch FROM subagents WHERE id = sqlc.arg(subagent_id)::uuid;

-- name: HasSubagentWork :one
SELECT EXISTS (SELECT 1 FROM subagents WHERE id = sqlc.arg(subagent_id)::uuid AND status = 'running');

-- name: ListRunningSubagents :many
SELECT id, user_id, started_at FROM subagents WHERE status = 'running' ORDER BY id;

-- name: CompleteSubagent :execrows
UPDATE subagents
SET status = 'completed', output = sqlc.arg(output)::text, finished_at = now()
WHERE id = sqlc.arg(subagent_id)::uuid AND status = 'running';

-- name: FailSubagent :execrows
UPDATE subagents
SET status = 'failed', error = sqlc.arg(error)::text, finished_at = now()
WHERE id = sqlc.arg(subagent_id)::uuid AND status = 'running';

-- name: CancelSubagent :execrows
-- 取消时同步递增代数，确保正在跑该 Subagent 的实例即使尚未收到 Redis 取消广播也无法再提交旧结果。
UPDATE subagents
SET status = 'cancelled', lease_epoch = lease_epoch + 1, finished_at = now()
WHERE id = sqlc.arg(subagent_id)::uuid AND user_id = sqlc.arg(user_id)::uuid AND status = 'running';
