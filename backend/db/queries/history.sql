-- 本文件是历史检索的查询集合：在当前用户允许范围内对 messages 全表做关键词搜索，并按 ID 读回原文。

-- name: SearchMessagesByKeyword :many
-- 关键词路：精确子串命中（中文不分词，靠 search_text 上的 pg_trgm 索引加速），按命中次数与新旧排序。
SELECT m.id, m.role, m.sender, m.is_summary, coalesce(m.sequence, 0)::bigint AS sequence, m.created_at,
    CASE WHEN (length(lower(m.search_text)) - length(replace(lower(m.search_text), lower(sqlc.arg(keyword)::text), ''))) > 0
        THEN (length(lower(m.search_text)) - length(replace(lower(m.search_text), lower(sqlc.arg(keyword)::text), ''))) / length(sqlc.arg(keyword)::text)
        ELSE 0 END::bigint AS keyword_hits,
    CASE WHEN sqlc.arg(keyword)::text = '' THEN left(m.search_text, 600)
        ELSE substring(m.search_text FROM greatest(1, strpos(lower(m.search_text), lower(sqlc.arg(keyword)::text)) - 200) FOR 600) END::text AS excerpt
FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id)
 AND m.content ->> 'name' IS DISTINCT FROM 'instructions'
 AND m.search_text <> ''
 AND (sqlc.arg(keyword)::text = '' OR strpos(lower(m.search_text), lower(sqlc.arg(keyword)::text)) > 0)
 AND (sqlc.arg(who)::text = 'any'
   OR (sqlc.arg(who)::text = 'user' AND m.sender = 'user')
   OR (sqlc.arg(who)::text = 'main' AND (m.role = 'assistant' OR m.sender = 'main')))
ORDER BY keyword_hits DESC, m.sequence DESC NULLS LAST, m.id DESC
LIMIT sqlc.arg(limit_count)::integer;

-- name: GetHistoryMessage :one
-- 按 ID 读回一条消息的提取文本；只限当前用户，查不到即视为不存在，不泄露其他用户的消息。
SELECT m.id, m.role, m.sender, m.is_summary, coalesce(m.sequence, 0)::bigint AS sequence, m.created_at, m.search_text
FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id)
 AND m.content ->> 'name' IS DISTINCT FROM 'instructions'
 AND m.id = sqlc.arg(message_id);
