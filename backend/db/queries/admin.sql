-- 本文件是管理看板的查询集合：提供时间线筛选所需的用户列表、Main 与 Subagent 状态，按游标读取指定 Agent 的消息时间线，并读取单条消息详情（sqlc 据此生成代码）。

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
-- 按 (user_id, subagent_id) 隔离读取指定 Agent 的消息时间线，使用单调不回退的 order_at 与序号游标翻页。
WITH user_messages AS MATERIALIZED (
	SELECT m.*, max(m.created_at) OVER (ORDER BY m.sequence) AS order_at
	FROM messages m
	WHERE m.user_id = sqlc.arg(user_id)::uuid
		AND m.subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid
		AND NOT m.generating
)
SELECT id, sequence::bigint AS sequence, created_at AS at, order_at::timestamptz AS order_at, role, content, sender, is_summary
FROM user_messages
WHERE created_at >= sqlc.arg(from_at)::timestamptz AND created_at < sqlc.arg(to_at)::timestamptz
	AND (sqlc.narg(cursor_at)::timestamptz IS NULL
		OR (order_at, sequence, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.arg(cursor_sequence)::bigint, sqlc.narg(cursor_id)::uuid))
ORDER BY order_at DESC, sequence DESC, id DESC LIMIT sqlc.arg(page_limit)::integer;

-- name: GetAdminTimelineMessage :one
SELECT role, content FROM messages WHERE id = $1;
