-- 本文件保存用户与长期工作区的绑定及当前实例位置；sqlc 生成类型，云端状态由 FC 判断。

-- name: GetSandboxWorkspace :one
SELECT * FROM sandbox_workspaces WHERE user_id = $1;

-- name: BindSandboxWorkspace :one
INSERT INTO sandbox_workspaces (user_id, server_addr, role_arn)
VALUES ($1, $2, $3)
ON CONFLICT (user_id) DO NOTHING
RETURNING *;

-- name: SaveSandboxInstance :exec
UPDATE sandbox_workspaces
SET sandbox_id = $2, token_ciphertext = $3, traffic_token_ciphertext = $4, updated_at = now()
WHERE user_id = $1;
