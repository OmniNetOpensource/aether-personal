-- 本文件为页面提供可见聊天搜索和原文附近的分页，所有查询始终限制为当前登录用户。

-- name: SearchChatMessages :many
-- 多取一条判断是否还有结果，ID 游标使新消息到来时已有分页仍然稳定。
SELECT m.id, m.role, m.created_at,
    substring(chat_message_text(m.content, m.role)
        FROM greatest(1, strpos(lower(chat_message_text(m.content, m.role)), lower(sqlc.arg(keyword)::text)) - 70)
        FOR greatest(240, length(sqlc.arg(keyword)::text) + 140))::text AS excerpt
FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id) AND m.visible AND m.id < sqlc.arg(before_id)::uuid
    AND chat_message_text(m.content, m.role) ILIKE sqlc.arg(pattern)::text
ORDER BY m.id DESC
LIMIT 31;

-- name: GetChatMessage :one
-- 先确认锚点属于当前用户且可见，再读取它两侧的消息。
SELECT m.id, m.role, m.sender, m.client_message_id, m.content, m.created_at,
    coalesce(m.chat_updated_sequence, m.sequence)::bigint AS change_cursor
FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id) AND m.visible AND m.id = sqlc.arg(message_id)
    AND (m.role <> 'assistant' OR chat_message_text(m.content, m.role) <> '');

-- name: ListChatContextBefore :many
SELECT m.id, m.role, m.sender, m.client_message_id, m.content, m.created_at,
    coalesce(m.chat_updated_sequence, m.sequence)::bigint AS change_cursor
FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id) AND m.visible AND m.id < sqlc.arg(message_id)
    AND (m.role <> 'assistant' OR chat_message_text(m.content, m.role) <> '')
ORDER BY m.id DESC LIMIT 21;

-- name: ListChatContextAfter :many
SELECT m.id, m.role, m.sender, m.client_message_id, m.content, m.created_at,
    coalesce(m.chat_updated_sequence, m.sequence)::bigint AS change_cursor
FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id) AND m.visible AND m.id > sqlc.arg(message_id)
    AND (m.role <> 'assistant' OR chat_message_text(m.content, m.role) <> '')
ORDER BY m.id LIMIT 21;
