-- 本文件按用户保存虚拟记忆文件，只有 memory 工具读取和编辑这些正文，请求组装不自动加载记忆。

-- name: EnsureMemoryRoot :exec
INSERT INTO memory_files (user_id, path, content)
VALUES ($1, '/memories/memory.md', E'# Memory\n\n## 核心约定\n\n## 主题索引\n')
ON CONFLICT (user_id, path) DO NOTHING;

-- name: ListMemoryFiles :many
SELECT path, octet_length(content)::bigint AS bytes FROM memory_files
WHERE user_id = $1 ORDER BY path;

-- name: GetMemoryFile :one
SELECT content FROM memory_files WHERE user_id = $1 AND path = $2;

-- name: CreateMemoryFile :exec
INSERT INTO memory_files (user_id, path, content) VALUES ($1, $2, $3);

-- name: UpdateMemoryFile :exec
UPDATE memory_files SET content = $3, updated_at = now() WHERE user_id = $1 AND path = $2;

-- name: DeleteMemoryPath :exec
DELETE FROM memory_files WHERE user_id = $1
AND (path = $2 OR starts_with(path, $2::text || '/'));

-- name: RenameMemoryPath :exec
UPDATE memory_files
SET path = sqlc.arg(new_path)::text || substr(path, length(sqlc.arg(old_path)::text) + 1), updated_at = now()
WHERE user_id = sqlc.arg(user_id)
AND (path = sqlc.arg(old_path)::text OR starts_with(path, sqlc.arg(old_path)::text || '/'));
