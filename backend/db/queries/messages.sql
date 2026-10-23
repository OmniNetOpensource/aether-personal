-- name: LockUserMessages :exec
-- 事务内持有，提交时释放：同一用户的插入排队进行，id 顺序与提交顺序一致。
SELECT pg_advisory_xact_lock(hashtextextended('messages:' || sqlc.arg(user_id)::uuid::text, 0));

-- name: TryLockRuntime :one
SELECT pg_try_advisory_lock(hashtextextended(sqlc.arg(lock_key)::text, 0));

-- name: UnlockRuntime :one
SELECT pg_advisory_unlock(hashtextextended(sqlc.arg(lock_key)::text, 0));

-- name: NotifyUserMessages :exec
SELECT pg_notify('user_messages', sqlc.arg(user_id)::uuid::text);

-- name: InsertUserMessage :execrows
INSERT INTO messages (user_id, sequence, role, content, sender, client_message_id)
VALUES (
    sqlc.arg(user_id),
    NULL,
    'user',
    jsonb_build_object('role', 'user', 'content', sqlc.arg(text)::text),
    'user',
    sqlc.arg(client_message_id)
)
ON CONFLICT (user_id, client_message_id) DO NOTHING;

-- name: InsertAgentMessage :exec
INSERT INTO messages (user_id, subagent_id, role, content, step_id)
VALUES ($1, $2, $3, $4, $5);

-- name: DeliverAgentMessage :exec
INSERT INTO messages (user_id, subagent_id, sequence, role, content, sender, sender_subagent_id)
VALUES ($1, $2, NULL, 'user', $3, $4, $5);

-- name: ListPendingAgentMessageIDs :many
SELECT id FROM messages
WHERE user_id = sqlc.arg(user_id) AND subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid AND sequence IS NULL
ORDER BY id;

-- name: MarkMessageRead :exec
UPDATE messages SET sequence = nextval('message_sequence') WHERE id = $1;

-- name: ListRunnableAgents :many
SELECT user_id, subagent_id FROM messages WHERE sequence IS NULL
UNION
SELECT user_id, subagent_id FROM steps WHERE finished_at IS NULL
UNION
SELECT user_id, id AS subagent_id FROM subagents WHERE status = 'running'
ORDER BY user_id, subagent_id NULLS FIRST;

-- name: ListAgentContext :many
SELECT content FROM messages
WHERE user_id = sqlc.arg(user_id) AND subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid AND sequence IS NOT NULL
ORDER BY sequence;

-- name: StartStep :one
INSERT INTO steps (user_id, subagent_id, model, started_at)
VALUES ($1, $2, $3, now())
RETURNING id;

-- name: GetUnfinishedStep :one
SELECT id FROM steps
WHERE user_id = sqlc.arg(user_id) AND subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid AND finished_at IS NULL
ORDER BY started_at LIMIT 1;

-- name: FinishStep :exec
UPDATE steps SET input_tokens = $2, output_tokens = $3, cost = $4, error = $5, finished_at = now() WHERE id = $1;

-- name: FailUnfinishedSteps :exec
UPDATE steps SET error = sqlc.arg(error), finished_at = now()
WHERE user_id = sqlc.arg(user_id) AND subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid AND finished_at IS NULL;

-- name: CreateSubagent :one
INSERT INTO subagents (user_id, status) VALUES ($1, 'idle') RETURNING id;

-- name: GetSubagent :one
SELECT * FROM subagents WHERE user_id = $1 AND id = $2;

-- name: StartSubagent :one
UPDATE subagents
SET started_at = CASE WHEN status = 'running' THEN COALESCE(started_at, now()) ELSE now() END,
    status = 'running'
WHERE user_id = $1 AND id = $2
RETURNING started_at;

-- name: SetSubagentStatus :exec
UPDATE subagents SET status = $3 WHERE user_id = $1 AND id = $2;

-- name: ListChatRowsAfter :many
SELECT id, role, sender, client_message_id, content, created_at FROM messages
WHERE user_id = $1 AND subagent_id IS NULL AND (sender = 'user' OR role = 'assistant') AND id > $2
ORDER BY id
LIMIT 100;

-- name: ListChatRowsBefore :many
SELECT id, role, sender, client_message_id, content, created_at FROM messages
WHERE user_id = $1 AND subagent_id IS NULL AND (sender = 'user' OR role = 'assistant') AND id < $2
ORDER BY id DESC
LIMIT 50;

-- name: GetLatestMainMessageID :one
SELECT id FROM messages
WHERE user_id = $1 AND subagent_id IS NULL
ORDER BY id DESC
LIMIT 1;

-- name: GetReadUpTo :one
SELECT id FROM messages
WHERE user_id = $1 AND subagent_id IS NULL AND sender = 'user' AND sequence IS NOT NULL
ORDER BY id DESC
LIMIT 1;
