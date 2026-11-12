-- 本文件是管理看板的查询集合：提供时间线筛选所需的用户、模型候选、Main 与 Subagent 状态，合并指定 Agent 的消息与模型请求时间线，并读取单次调用或单条消息详情（sqlc 据此生成代码）。

-- name: AdminSnapshot :one
-- 返回时间窗口内出现过的模型候选，供时间线筛选输入框提示。
WITH models AS (
 SELECT c.requested_model AS model
 FROM model_calls c
 WHERE c.started_at >= sqlc.arg(from_at)::timestamptz AND c.started_at < sqlc.arg(to_at)::timestamptz
  AND (sqlc.narg(user_id)::uuid IS NULL OR c.user_id = sqlc.narg(user_id)::uuid)
 GROUP BY c.requested_model
 ORDER BY count(*) DESC, c.requested_model
 LIMIT 100
)
SELECT jsonb_build_object(
 'generated_at', now(),
 'models', (SELECT coalesce(jsonb_agg(to_jsonb(models)), '[]'::jsonb) FROM models)
)::jsonb AS payload;

-- name: ListAdminUsers :many
-- 列出用户供看板下拉框选择，按创建时间倒序排列。
SELECT u.id, u.email, u.created_at
FROM users u
WHERE (sqlc.narg(user_id)::uuid IS NULL OR u.id = sqlc.narg(user_id)::uuid)
ORDER BY u.created_at DESC, u.id DESC
LIMIT 200;

-- name: GetAdminAgentStatus :one
-- 返回选中用户 Main 的当前运行状态（含后台 Subagent 运行中）。
SELECT u.id AS user_id, u.email,
 CASE
  WHEN EXISTS (SELECT 1 FROM main_work w WHERE w.user_id = u.id)
    OR EXISTS (SELECT 1 FROM subagents sa WHERE sa.user_id = u.id AND sa.status = 'running') THEN 'processing'
  WHEN u.main_failed_through_sequence >= (SELECT max(m.sequence) FROM messages m WHERE m.user_id = u.id AND m.subagent_id IS NULL) THEN 'failed'
  ELSE 'idle'
 END::text AS status
FROM users u
WHERE u.id = sqlc.arg(selected_user_id)::uuid;

-- name: ListAdminSubagents :many
-- 列出选中用户的近期 Subagent，供看板在 Main 与各 Subagent 之间切换查看独立轨迹。
SELECT id, title, status, started_at, finished_at
FROM subagents
WHERE user_id = sqlc.arg(selected_user_id)::uuid
ORDER BY id DESC
LIMIT 100;

-- name: GetAdminCallDetail :one
SELECT request_body, response_body, response_truncated FROM model_calls WHERE id = $1;

-- name: ListAdminTimeline :many
-- 按 (user_id, subagent_id) 隔离读取指定 Agent 的消息与未产出消息的请求，共用同一游标翻页。
WITH user_messages AS MATERIALIZED (
 SELECT m.*, max(m.created_at) OVER (ORDER BY m.sequence) AS order_at
 FROM messages m
 WHERE m.user_id = sqlc.arg(user_id)::uuid
  AND m.subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid
  AND NOT m.generating
), calls AS MATERIALIZED (
 SELECT c.*, jsonb_build_object('id',c.id,'step_id',c.step_id,
  'requested_model',c.requested_model,'response_model',c.response_model,
  'outcome',c.outcome,'http_status',c.http_status,'purpose',c.purpose,
  'compact_reason',c.compact_reason,'context_tokens_before',c.context_tokens_before,
  'summary_through_sequence',c.summary_through_sequence,
  'failure_kind',c.failure_kind,'started_at',c.started_at,'finished_at',c.finished_at,'duration_ms',c.duration_ms,
  'input_tokens',c.input_tokens,'output_tokens',c.output_tokens,'cost',c.cost::text,
  'tool_names',to_jsonb(c.tool_names)) AS payload
 FROM model_calls c
 WHERE c.user_id = sqlc.arg(user_id)::uuid
  AND c.subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid
  AND (sqlc.narg(model)::text IS NULL OR c.requested_model = sqlc.narg(model)::text)
  AND (sqlc.narg(outcome)::text IS NULL OR c.outcome = sqlc.narg(outcome)::text)
), nodes AS (
 SELECT m.id, m.sequence::bigint AS sequence, m.created_at AS at, m.order_at,
  m.role, m.content, m.sender, m.is_summary, 'message'::text AS type, c.payload AS call
 FROM user_messages m LEFT JOIN LATERAL (
  SELECT mc.payload FROM calls mc WHERE (m.role = 'assistant' OR m.is_summary) AND mc.step_id = m.step_id AND mc.outcome = 'succeeded'
  ORDER BY mc.started_at DESC, mc.id DESC LIMIT 1
 ) c ON true
 WHERE m.created_at >= sqlc.arg(from_at)::timestamptz AND m.created_at < sqlc.arg(to_at)::timestamptz
  AND (sqlc.narg(model)::text IS NULL AND sqlc.narg(outcome)::text IS NULL OR EXISTS (
   SELECT 1 FROM calls mc WHERE mc.step_id = m.step_id OR (
    -- 用户输入没有 step_id，关联从收到输入到下一条模型回复所经历的请求尝试。
    m.step_id IS NULL AND mc.started_at >= m.created_at
    AND mc.started_at <= coalesce((
     SELECT coalesce((SELECT max(request.started_at) FROM model_calls request WHERE request.step_id = reply.step_id), reply.created_at)
     FROM user_messages reply WHERE reply.sequence > m.sequence AND reply.role = 'assistant'
     ORDER BY reply.sequence LIMIT 1
    ), 'infinity'::timestamptz)
   )
  ))
 UNION ALL
 SELECT c.id, 0::bigint, c.started_at, c.started_at,
  ''::text, '{}'::jsonb, NULL::text, false, 'call'::text, c.payload
 FROM calls c WHERE c.outcome <> 'succeeded'
  AND c.started_at >= sqlc.arg(from_at)::timestamptz AND c.started_at < sqlc.arg(to_at)::timestamptz
)
SELECT id, sequence, at, order_at::timestamptz AS order_at, role, content, sender, is_summary, type, call::jsonb AS call
FROM nodes
WHERE sqlc.narg(cursor_at)::timestamptz IS NULL
 OR (order_at, sequence, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.arg(cursor_sequence)::bigint, sqlc.narg(cursor_id)::uuid)
ORDER BY order_at DESC, sequence DESC, id DESC LIMIT sqlc.arg(page_limit)::integer;

-- name: GetAdminTimelineMessage :one
SELECT role, content FROM messages WHERE id = $1;
