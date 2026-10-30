-- 本文件是历史检索的查询集合：在当前用户允许范围内对 messages 全表做关键词与向量搜索、按 ID 读回原文，并为向量补算提供待算清单。

-- name: SearchMessagesByKeyword :many
-- 关键词路：精确子串命中（中文不分词，靠 search_text 上的 pg_trgm 索引加速），按命中次数与新旧排序。
-- 范围由 all_agents 与 subagent_id 决定：Main 传 all_agents=true 搜该用户全部列表，Subagent 传自己的 ID 只搜自己。
SELECT m.id, m.subagent_id, m.role, m.sender, m.is_summary, coalesce(m.sequence, 0)::bigint AS sequence, m.created_at,
    CASE WHEN (length(lower(m.search_text)) - length(replace(lower(m.search_text), lower(sqlc.arg(keyword)::text), ''))) > 0
        THEN (length(lower(m.search_text)) - length(replace(lower(m.search_text), lower(sqlc.arg(keyword)::text), ''))) / length(sqlc.arg(keyword)::text)
        ELSE 0 END::bigint AS keyword_hits,
    CASE WHEN sqlc.arg(keyword)::text = '' THEN left(m.search_text, 600)
        ELSE substring(m.search_text FROM greatest(1, strpos(lower(m.search_text), lower(sqlc.arg(keyword)::text)) - 200) FOR 600) END::text AS excerpt
FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id)
 AND (sqlc.arg(all_agents)::boolean OR m.subagent_id = sqlc.narg(subagent_id)::uuid)
 AND m.search_text <> ''
 AND (sqlc.arg(keyword)::text = '' OR strpos(lower(m.search_text), lower(sqlc.arg(keyword)::text)) > 0)
 AND (sqlc.arg(who)::text = 'any'
   OR (sqlc.arg(who)::text = 'user' AND m.sender = 'user')
   OR (sqlc.arg(who)::text = 'main' AND ((m.subagent_id IS NULL AND m.role = 'assistant') OR m.sender = 'main'))
   OR (sqlc.arg(who)::text = 'subagent' AND ((m.subagent_id IS NOT NULL AND m.role = 'assistant') OR m.sender = 'subagent')))
ORDER BY keyword_hits DESC, m.sequence DESC NULLS LAST, m.id DESC
LIMIT sqlc.arg(limit_count)::integer;

-- name: SearchMessagesByVector :many
-- 向量路：按余弦距离找语义相近的消息，只包含已补算向量的行；与关键词路的结果在 Go 代码里按排名合并。
SELECT m.id, m.subagent_id, m.role, m.sender, m.is_summary, coalesce(m.sequence, 0)::bigint AS sequence, m.created_at,
    (m.embedding <=> sqlc.arg(query_embedding)::vector)::float8 AS distance,
    left(m.search_text, 600) AS excerpt
FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id)
 AND (sqlc.arg(all_agents)::boolean OR m.subagent_id = sqlc.narg(subagent_id)::uuid)
 AND m.search_text <> ''
 AND m.embedding IS NOT NULL
 AND (sqlc.arg(who)::text = 'any'
   OR (sqlc.arg(who)::text = 'user' AND m.sender = 'user')
   OR (sqlc.arg(who)::text = 'main' AND ((m.subagent_id IS NULL AND m.role = 'assistant') OR m.sender = 'main'))
   OR (sqlc.arg(who)::text = 'subagent' AND ((m.subagent_id IS NOT NULL AND m.role = 'assistant') OR m.sender = 'subagent')))
ORDER BY m.embedding <=> sqlc.arg(query_embedding)::vector
LIMIT sqlc.arg(limit_count)::integer;

-- name: GetHistoryMessage :one
-- 按 ID 读回一条消息的提取文本与归属信息；范围校验与搜索相同，查不到即视为不存在，不泄露其他用户或越界 Subagent 的消息。
SELECT m.id, m.subagent_id, m.role, m.sender, m.is_summary, coalesce(m.sequence, 0)::bigint AS sequence, m.created_at, m.search_text
FROM messages AS m
WHERE m.user_id = sqlc.arg(user_id)
 AND (sqlc.arg(all_agents)::boolean OR m.subagent_id = sqlc.narg(subagent_id)::uuid)
 AND m.id = sqlc.arg(message_id);

-- name: ListMessagesNeedingEmbedding :many
-- 向量补算清单：还没有向量且有可搜索文本的消息，按 ID（即写入先后）逐批取。
SELECT m.id, m.search_text FROM messages AS m
WHERE m.embedding IS NULL AND m.search_text <> ''
ORDER BY m.id
LIMIT sqlc.arg(limit_count)::integer;

-- name: UpdateMessageEmbedding :exec
UPDATE messages SET embedding = sqlc.arg(embedding)::vector WHERE id = sqlc.arg(message_id) AND embedding IS NULL;
