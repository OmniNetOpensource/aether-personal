-- 本文件是消息与 Agent 运行时的核心查询集合：消息写入与读取、上下文读取、步骤记账、运行时锁与“是否有活”判断，是对话主循环的数据基础（sqlc 据此生成代码）。

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
INSERT INTO messages (user_id, role, content, sender, client_message_id, visible)
VALUES (
    sqlc.arg(user_id),
    'user',
    sqlc.arg(content)::jsonb,
    'user',
    sqlc.arg(client_message_id),
    true
)
ON CONFLICT (user_id, client_message_id) DO NOTHING;

-- name: InsertAgentMessage :exec
INSERT INTO messages (user_id, role, content, step_id, sender, visible)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListRunnableAgents :many
SELECT user_id FROM main_work ORDER BY user_id;

-- name: HasMainWork :one
-- 与调度共用 main_work 的定义：聊天的思考状态、运行前的检查都看它。
SELECT EXISTS (SELECT 1 FROM main_work WHERE user_id = sqlc.arg(user_id)::uuid);

-- name: MarkMainFailed :exec
-- 记下失败时最后一条消息的序号，之后有新消息才再次运行。
UPDATE users SET main_failed_through_sequence = (SELECT max(m.sequence) FROM messages AS m WHERE m.user_id = sqlc.arg(user_id)::uuid)
WHERE id = sqlc.arg(user_id)::uuid;

-- name: ListAgentContextRows :many
WITH summary AS (
 SELECT id, summary_through_sequence FROM messages
 WHERE user_id = sqlc.arg(user_id) AND is_summary
 ORDER BY sequence DESC LIMIT 1
)
SELECT m.id, m.sequence, m.content, m.is_summary, m.sender, m.step_id, m.created_at FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id)
 AND (
  m.id = (SELECT id FROM summary)
  OR (NOT m.is_summary AND m.sequence > coalesce((SELECT summary_through_sequence FROM summary), 0))
 )
ORDER BY m.is_summary DESC, m.sequence;

-- name: StartStep :one
INSERT INTO steps (user_id, model, started_at)
VALUES ($1, $2, now())
RETURNING id;

-- name: FinishStep :exec
UPDATE steps SET input_tokens = $2, output_tokens = $3, cost = $4, error = $5, finished_at = now() WHERE id = $1;

-- name: FailUnfinishedSteps :exec
UPDATE steps SET error = sqlc.arg(error), finished_at = now()
WHERE user_id = sqlc.arg(user_id) AND finished_at IS NULL;

-- name: ListChatRowsAfter :many
SELECT id, role, sender, client_message_id, content, created_at FROM messages
WHERE user_id = $1 AND visible AND id > $2
ORDER BY id
LIMIT 100;

-- name: ListChatRowsBefore :many
SELECT id, role, sender, client_message_id, content, created_at FROM messages
WHERE user_id = $1 AND visible AND id < $2
ORDER BY id DESC
LIMIT 50;

-- name: GetLatestMainMessageID :one
SELECT id FROM messages
WHERE user_id = $1
ORDER BY id DESC
LIMIT 1;

-- name: StartCompactStep :one
INSERT INTO steps (user_id, model, started_at, purpose)
VALUES ($1, $2, now(), 'compact') RETURNING id;

-- name: InsertContextSummary :exec
INSERT INTO messages (user_id, role, content, step_id, is_summary, summary_through_sequence)
VALUES ($1, 'user', $2, $3, true, $4);

-- name: GetLastContextUsage :one
SELECT requested_model, coalesce(input_tokens, 0)::integer AS input_tokens
FROM model_calls WHERE user_id = sqlc.arg(user_id)
 AND purpose = 'agent' AND outcome = 'succeeded' AND input_tokens IS NOT NULL AND request_body IS NOT NULL
ORDER BY started_at DESC LIMIT 1;
