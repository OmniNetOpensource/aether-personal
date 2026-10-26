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
SELECT candidates.user_id, candidates.subagent_id FROM (
    SELECT user_id, subagent_id FROM messages WHERE sequence IS NULL
    UNION
    SELECT user_id, subagent_id FROM steps WHERE finished_at IS NULL
    UNION
    SELECT user_id, id AS subagent_id FROM subagents WHERE status = 'running'
    UNION
    SELECT user_id, subagent_id FROM model_calls WHERE outcome = 'in_flight' AND purpose <> 'recap'
) AS candidates
WHERE NOT EXISTS (
    SELECT 1 FROM steps AS deferred
    WHERE deferred.user_id = candidates.user_id
        AND deferred.subagent_id IS NOT DISTINCT FROM candidates.subagent_id
        AND deferred.finished_at IS NULL AND deferred.resume_after > now()
)
ORDER BY candidates.user_id, candidates.subagent_id NULLS FIRST;

-- name: ListAgentContext :many
WITH summary AS (
 SELECT id, summary_through_sequence FROM messages
 WHERE user_id = sqlc.arg(user_id) AND subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid AND is_summary
 ORDER BY sequence DESC LIMIT 1
)
SELECT m.content FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id) AND m.subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid
 AND m.sequence IS NOT NULL AND (
  m.id = (SELECT id FROM summary)
  OR (NOT m.is_summary AND m.sequence > coalesce((SELECT summary_through_sequence FROM summary), 0))
 )
ORDER BY m.is_summary DESC, m.sequence;

-- name: ListAgentContextRows :many
WITH summary AS (
 SELECT id, summary_through_sequence FROM messages
 WHERE user_id = sqlc.arg(user_id) AND subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid AND is_summary
 ORDER BY sequence DESC LIMIT 1
)
SELECT coalesce(m.sequence, 0)::bigint AS sequence, m.content, m.is_summary, m.compact_reply_sequence FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id) AND m.subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid
 AND m.sequence IS NOT NULL AND (
  m.id = (SELECT id FROM summary)
  OR (NOT m.is_summary AND m.sequence > coalesce((SELECT summary_through_sequence FROM summary), 0))
 )
ORDER BY m.is_summary DESC, m.sequence;

-- name: StartStep :one
INSERT INTO steps (user_id, subagent_id, model, started_at)
VALUES ($1, $2, $3, now())
RETURNING id;

-- name: GetUnfinishedStep :one
SELECT id FROM steps
WHERE user_id = sqlc.arg(user_id) AND subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid AND finished_at IS NULL AND purpose = 'agent'
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
WHERE user_id = $1 AND subagent_id IS NULL AND NOT is_summary AND (sender = 'user' OR role = 'assistant') AND id > $2
ORDER BY id
LIMIT 100;

-- name: ListChatRowsBefore :many
SELECT id, role, sender, client_message_id, content, created_at FROM messages
WHERE user_id = $1 AND subagent_id IS NULL AND NOT is_summary AND (sender = 'user' OR role = 'assistant') AND id < $2
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

-- name: IsMainThinking :one
SELECT EXISTS (
    SELECT 1 FROM steps AS main_step
    WHERE main_step.user_id = sqlc.arg(user_id)::uuid
        AND main_step.subagent_id IS NULL AND main_step.finished_at IS NULL
    UNION ALL
    SELECT 1 FROM messages AS main_message
    WHERE main_message.user_id = sqlc.arg(user_id)::uuid
        AND main_message.subagent_id IS NULL AND main_message.sequence IS NULL
);

-- name: StartCompactStep :one
INSERT INTO steps (user_id, subagent_id, model, started_at, purpose)
VALUES ($1, $2, $3, now(), 'compact') RETURNING id;

-- name: RecoverCompactSteps :exec
UPDATE steps SET finished_at = now(), error = 'compaction interrupted before commit'
WHERE user_id = sqlc.arg(user_id) AND subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid
 AND purpose = 'compact' AND finished_at IS NULL;

-- name: InsertContextSummary :exec
INSERT INTO messages (user_id, subagent_id, role, content, step_id, is_summary, summary_through_sequence, compact_reply_sequence)
VALUES ($1, $2, 'user', $3, $4, true, $5, $6);

-- name: GetLastMainReply :one
SELECT coalesce(m.sequence, 0)::bigint AS sequence, m.created_at FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id) AND m.subagent_id IS NULL AND m.role = 'assistant'
 AND EXISTS (
  SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(m.content->'tool_calls') = 'array'
   THEN m.content->'tool_calls' ELSE '[]'::jsonb END) AS call
  JOIN messages AS receipt ON receipt.user_id = m.user_id AND receipt.subagent_id IS NULL
   AND receipt.role = 'tool' AND receipt.content->>'tool_call_id' = call->>'id'
   AND receipt.content->>'content' = 'delivered' AND receipt.sequence > m.sequence
  WHERE call->'function'->>'name' = 'send_message'
   AND CASE WHEN pg_input_is_valid(call->'function'->>'arguments', 'jsonb')
    THEN (call->'function'->>'arguments')::jsonb->>'to' = 'user' ELSE false END
 )
ORDER BY m.sequence DESC LIMIT 1;

-- name: GetLastContextUsage :one
SELECT requested_model, request_body, coalesce(input_tokens, 0)::integer AS input_tokens
FROM model_calls WHERE user_id = sqlc.arg(user_id)
 AND subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid
 AND purpose = 'agent' AND outcome = 'succeeded' AND input_tokens IS NOT NULL AND request_body IS NOT NULL
ORDER BY started_at DESC LIMIT 1;

-- name: SaveCompactedContextSize :exec
UPDATE model_calls SET context_tokens_after = $2 WHERE step_id = $1 AND purpose = 'compact' AND outcome = 'succeeded';
