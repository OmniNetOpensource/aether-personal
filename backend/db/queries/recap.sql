-- 本文件是 recap 行动摘要的查询集合：维护摘要状态、领取待摘要用户、保存摘要，并记录步骤活动供摘要引用（sqlc 据此生成代码）。

-- name: ListRecapUsers :many
SELECT user_id FROM active_agent_users
UNION SELECT user_id FROM recap_state WHERE active;

-- name: SyncRecapState :one
INSERT INTO recap_state (user_id, active, work_version)
VALUES (sqlc.arg(user_id), EXISTS (SELECT 1 FROM active_agent_users WHERE active_agent_users.user_id = sqlc.arg(user_id)), 1)
ON CONFLICT (user_id) DO UPDATE SET
    active = EXCLUDED.active,
    work_version = recap_state.work_version + CASE WHEN EXCLUDED.active AND NOT recap_state.active THEN 1 ELSE 0 END,
    window_started_at = CASE WHEN recap_state.active <> EXCLUDED.active THEN now() ELSE recap_state.window_started_at END,
    request_total = CASE WHEN recap_state.active <> EXCLUDED.active THEN 0 ELSE recap_state.request_total END,
    recap_request_total = CASE WHEN recap_state.active <> EXCLUDED.active THEN 0 ELSE recap_state.recap_request_total END,
    summary = CASE WHEN recap_state.active <> EXCLUDED.active THEN '' ELSE recap_state.summary END,
    revision = recap_state.revision + CASE WHEN recap_state.active <> EXCLUDED.active THEN 1 ELSE 0 END,
    updated_at = CASE WHEN recap_state.active <> EXCLUDED.active THEN now() ELSE recap_state.updated_at END
RETURNING *;

-- name: CountRecapRequest :one
INSERT INTO recap_state (user_id, request_total)
SELECT steps.user_id, 1 FROM steps WHERE steps.id = sqlc.arg(step_id)
ON CONFLICT (user_id) DO UPDATE SET
    active = true,
    work_version = recap_state.work_version + CASE WHEN recap_state.active THEN 0 ELSE 1 END,
    window_started_at = CASE WHEN recap_state.active THEN recap_state.window_started_at ELSE now() END,
    request_total = CASE WHEN recap_state.active THEN recap_state.request_total + 1 ELSE 1 END,
    recap_request_total = CASE WHEN recap_state.active THEN recap_state.recap_request_total ELSE 0 END,
    summary = CASE WHEN recap_state.active THEN recap_state.summary ELSE '' END,
    revision = recap_state.revision + CASE WHEN recap_state.active THEN 0 ELSE 1 END
RETURNING recap_state.user_id;

-- name: ClaimRecap :one
WITH eligible AS (
    SELECT recap_state.user_id, recap_state.window_started_at AS since_at FROM recap_state
    WHERE recap_state.user_id = sqlc.arg(user_id) AND active
    AND (window_started_at <= now() - interval '20 seconds' OR request_total - recap_request_total >= 4)
    AND EXISTS (SELECT 1 FROM active_agent_users WHERE active_agent_users.user_id = sqlc.arg(user_id))
    FOR UPDATE
)
UPDATE recap_state SET window_started_at = now(), recap_request_total = request_total
FROM eligible WHERE recap_state.user_id = eligible.user_id
RETURNING recap_state.*, eligible.since_at;

-- name: ListDueRecapUsers :many
SELECT user_id FROM recap_state WHERE active
    AND (window_started_at <= now() - interval '20 seconds' OR request_total - recap_request_total >= 4)
ORDER BY window_started_at, user_id;

-- name: SaveRecap :execrows
UPDATE recap_state SET summary = sqlc.arg(summary), revision = revision + 1, updated_at = now()
WHERE recap_state.user_id = sqlc.arg(user_id) AND work_version = sqlc.arg(work_version) AND active
    AND EXISTS (SELECT 1 FROM active_agent_users WHERE active_agent_users.user_id = sqlc.arg(user_id));

-- name: GetRecapState :one
SELECT active AND EXISTS (SELECT 1 FROM active_agent_users WHERE active_agent_users.user_id = recap_state.user_id) AS active,
    summary, revision, work_version, updated_at
FROM recap_state WHERE recap_state.user_id = $1;

-- name: RecordStepActivity :exec
UPDATE steps SET activity = (
    SELECT coalesce(jsonb_agg(entry ORDER BY ordinal), '[]'::jsonb) FROM (
        SELECT entry, ordinal FROM jsonb_array_elements(steps.activity || jsonb_build_array(sqlc.arg(activity)::jsonb))
            WITH ORDINALITY AS items(entry, ordinal)
        ORDER BY ordinal DESC LIMIT 8
    ) AS recent
)
WHERE id = sqlc.arg(step_id) AND finished_at IS NULL;

-- name: GetRecapInput :one
SELECT jsonb_build_object(
    'user_request', (SELECT left(content->>'content', 2000) FROM messages
        WHERE messages.user_id = sqlc.arg(user_id) AND subagent_id IS NULL
            AND (sender = 'user' OR (sender = 'system' AND content->>'name' = 'schedule_event'))
        ORDER BY id DESC LIMIT 1),
    'previous_summary', (SELECT summary FROM recap_state WHERE recap_state.user_id = sqlc.arg(user_id)),
    'active_agents', (SELECT coalesce(jsonb_agg(item), '[]'::jsonb) FROM (
        SELECT jsonb_build_object('agent', CASE WHEN s.subagent_id IS NULL THEN 'Main' ELSE s.subagent_id::text END,
            'task', CASE WHEN s.subagent_id IS NOT NULL THEN (SELECT left(m.content->>'content', 1200) FROM messages m
                WHERE m.user_id = s.user_id AND m.subagent_id = s.subagent_id AND m.sender = 'main' ORDER BY m.id LIMIT 1) END,
            'activity', s.activity) AS item
        FROM steps s WHERE s.user_id = sqlc.arg(user_id) AND s.finished_at IS NULL
        ORDER BY s.started_at LIMIT 32
    ) agents),
    'queued_agents', (SELECT count(DISTINCT subagent_id) FROM messages WHERE messages.user_id = sqlc.arg(user_id) AND sequence IS NULL),
    'recent_steps', (SELECT coalesce(jsonb_agg(item ORDER BY started_at), '[]'::jsonb) FROM (
        SELECT s.started_at, jsonb_build_object('agent', CASE WHEN s.subagent_id IS NULL THEN 'Main' ELSE s.subagent_id::text END,
            'activity', s.activity, 'stopped_with_error', s.error IS NOT NULL) AS item
        FROM steps s WHERE s.user_id = sqlc.arg(user_id) AND s.finished_at IS NOT NULL
            AND s.finished_at >= sqlc.arg(since_at)::timestamptz
        ORDER BY s.started_at DESC LIMIT 8
    ) recent)
)::jsonb AS payload;

-- name: StartRecapModelCall :one
INSERT INTO model_calls (user_id, requested_model, request_body, purpose)
VALUES (sqlc.arg(user_id), sqlc.arg(requested_model), sqlc.arg(request_body)::jsonb, 'recap') RETURNING id;

-- name: InterruptRecapModelCalls :exec
UPDATE model_calls SET outcome = 'interrupted', finished_at = now(), failure_kind = 'interrupted'
WHERE user_id = $1 AND purpose = 'recap' AND outcome = 'in_flight';
