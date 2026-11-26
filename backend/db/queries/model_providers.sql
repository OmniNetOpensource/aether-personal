-- 本文件管理自定义模型供应商表的读写查询：支持列出、按 ID 查询、新增、修改与删除（sqlc 据此生成代码）。

-- name: ListModelProviders :many
SELECT id, name, protocol, base_url, api_key_ciphertext, key_suffix, created_at, updated_at
FROM model_providers
ORDER BY created_at ASC, id ASC;

-- name: GetModelProvider :one
SELECT id, name, protocol, base_url, api_key_ciphertext, key_suffix, created_at, updated_at
FROM model_providers
WHERE id = $1;

-- name: CreateModelProvider :one
INSERT INTO model_providers (id, name, protocol, base_url, api_key_ciphertext, key_suffix)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, name, protocol, base_url, api_key_ciphertext, key_suffix, created_at, updated_at;

-- name: UpdateModelProvider :one
UPDATE model_providers
SET name = $2,
    protocol = $3,
    base_url = $4,
    api_key_ciphertext = $5,
    key_suffix = $6,
    updated_at = now()
WHERE id = $1
RETURNING id, name, protocol, base_url, api_key_ciphertext, key_suffix, created_at, updated_at;

-- name: DeleteModelProvider :execrows
DELETE FROM model_providers WHERE id = $1;
