-- 本文件是管理看板的查询集合：提供时间线筛选所需的用户列表、步骤用量汇总、Main 与 Subagent 状态，按游标读取带单步用量的消息时间线，并读取单条消息详情（sqlc 据此生成代码）。

-- name: GetAdminStepUsage :one
-- 汇总筛选窗口内的步骤数、输入/缓存/输出 Token 与费用；未选用户时汇总全站，选用户后按 Main 或指定 Subagent 隔离。
SELECT
	count(*)::bigint AS steps_count,
	count(*) FILTER (WHERE s.purpose IN ('compact', 'hot_compact'))::bigint AS compact_steps,
	count(*) FILTER (WHERE s.error IS NOT NULL)::bigint AS failed_steps,
	coalesce(sum(s.input_tokens), 0)::bigint AS input_tokens,
	coalesce(sum(s.cached_input_tokens), 0)::bigint AS cached_input_tokens,
	coalesce(sum(s.input_tokens) FILTER (WHERE s.cached_input_tokens IS NOT NULL), 0)::bigint AS cached_measured_input_tokens,
	count(*) FILTER (WHERE s.cached_input_tokens IS NOT NULL)::bigint AS cached_steps,
	coalesce(sum(s.output_tokens), 0)::bigint AS output_tokens,
	coalesce(sum(s.cost), 0)::numeric AS cost,
	count(*) FILTER (WHERE s.cost IS NOT NULL)::bigint AS cost_steps
FROM steps s
WHERE s.started_at >= sqlc.arg(from_at)::timestamptz
	AND s.started_at < sqlc.arg(to_at)::timestamptz
	AND (
		sqlc.narg(user_id)::uuid IS NULL
		OR (s.user_id = sqlc.narg(user_id)::uuid AND s.subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid)
	);

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

-- name: ListAdminTimeline :many
-- 按 (user_id, subagent_id) 隔离读取指定 Agent 的消息时间线，关联模型回复与压缩摘要对应的步骤用量，使用单调不回退的 order_at 与序号游标翻页。
WITH user_messages AS MATERIALIZED (
	SELECT m.*, max(m.created_at) OVER (ORDER BY m.sequence) AS order_at
	FROM messages m
	WHERE m.user_id = sqlc.arg(user_id)::uuid
		AND m.subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid
		AND NOT m.generating
)
SELECT m.id, m.sequence::bigint AS sequence, m.created_at AS at, m.order_at::timestamptz AS order_at,
	m.role, m.content, m.sender, m.is_summary,
	s.model AS step_model, s.purpose AS step_purpose,
	s.input_tokens AS step_input_tokens, s.cached_input_tokens AS step_cached_input_tokens,
	s.output_tokens AS step_output_tokens, s.cost AS step_cost
FROM user_messages m
LEFT JOIN steps s ON (m.role = 'assistant' OR m.is_summary) AND s.id = m.step_id
WHERE m.created_at >= sqlc.arg(from_at)::timestamptz AND m.created_at < sqlc.arg(to_at)::timestamptz
	AND (sqlc.narg(cursor_at)::timestamptz IS NULL
		OR (m.order_at, m.sequence, m.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.arg(cursor_sequence)::bigint, sqlc.narg(cursor_id)::uuid))
ORDER BY m.order_at DESC, m.sequence DESC, m.id DESC LIMIT sqlc.arg(page_limit)::integer;

-- name: GetAdminTimelineMessage :one
SELECT role, content FROM messages WHERE id = $1;
