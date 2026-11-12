-- 本文件提供对话消息公开分享的创建、归属校验、按令牌公开读取、用户分享列表查询与取消分享操作。

-- name: CreateMessageShare :one
INSERT INTO message_shares (user_id, token)
VALUES (sqlc.arg(user_id), sqlc.arg(token))
RETURNING id, token, created_at;

-- name: ListOwnedShareableMessages :many
-- 只允许分享当前用户主对话中已完成且对聊天可见的消息，按原消息位置排序。
SELECT m.id, m.role, m.sender, m.client_message_id, m.content, m.created_at,
    coalesce(m.chat_updated_sequence, m.sequence)::bigint AS change_cursor
FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id)
    AND m.subagent_id IS NULL
    AND m.visible
    AND NOT m.generating
    AND m.id = ANY(sqlc.arg(message_ids)::uuid[])
ORDER BY m.id;

-- name: InsertMessageShareItems :exec
INSERT INTO message_share_items (share_id, message_id)
SELECT sqlc.arg(share_id), unnest(sqlc.arg(message_ids)::uuid[]);

-- name: GetMessageShareByToken :one
SELECT id, token, created_at
FROM message_shares
WHERE token = sqlc.arg(token);

-- name: ListSharedMessageRows :many
SELECT m.id, m.role, m.sender, m.client_message_id, m.content, m.created_at,
    coalesce(m.chat_updated_sequence, m.sequence)::bigint AS change_cursor
FROM message_share_items AS i
JOIN messages AS m ON m.id = i.message_id
WHERE i.share_id = sqlc.arg(share_id)
    AND m.subagent_id IS NULL
    AND m.visible
    AND NOT m.generating
ORDER BY m.id;

-- name: ListUserMessageShares :many
-- 按创建时间倒序返回当前用户的分享记录、包含消息数与首条消息摘录，供管理列表展示。
SELECT s.id, s.token, s.created_at,
    coalesce((SELECT count(*) FROM message_share_items AS i WHERE i.share_id = s.id), 0)::integer AS message_count,
    coalesce((
        SELECT left(regexp_replace(chat_message_text(m.content, m.role), '\s+', ' ', 'g'), 120)
        FROM message_share_items AS i
        JOIN messages AS m ON m.id = i.message_id
        WHERE i.share_id = s.id
        ORDER BY m.id
        LIMIT 1
    ), '')::text AS preview
FROM message_shares AS s
WHERE s.user_id = sqlc.arg(user_id)
ORDER BY s.id DESC
LIMIT 100;

-- name: DeleteUserMessageShare :execrows
DELETE FROM message_shares
WHERE id = sqlc.arg(share_id) AND user_id = sqlc.arg(user_id);
