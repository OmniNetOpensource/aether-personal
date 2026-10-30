-- 本文件查询服务配置：读取当前凭证、保存管理员修改，以及一次性导入旧环境变量而不覆盖已有记录。

-- name: GetServiceSettings :one
SELECT * FROM service_settings WHERE service = $1;

-- name: ListServiceSettings :many
SELECT * FROM service_settings ORDER BY service;

-- name: SaveServiceSettings :exec
INSERT INTO service_settings (service, api_key_ciphertext, key_suffix, base_url, email_from)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (service) DO UPDATE SET api_key_ciphertext = EXCLUDED.api_key_ciphertext,
    key_suffix = EXCLUDED.key_suffix, base_url = EXCLUDED.base_url,
    email_from = EXCLUDED.email_from, updated_at = now();

-- name: ImportServiceSettings :execrows
INSERT INTO service_settings (service, api_key_ciphertext, key_suffix, base_url, email_from)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (service) DO NOTHING;
