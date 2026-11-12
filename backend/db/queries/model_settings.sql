-- 本文件读取和保存账号模型选择及全局运行参数；版本校验在写入语句内完成，避免覆盖其他设备的保存。

-- name: GetUserModelSettings :one
SELECT main_model, main_context_length, subagent_model, subagent_context_length, compact_model, version
FROM user_model_settings WHERE user_id = $1;

-- name: SaveUserModelSettings :one
INSERT INTO user_model_settings (user_id, main_model, main_context_length,
    subagent_model, subagent_context_length, compact_model, version)
SELECT sqlc.arg(user_id), sqlc.arg(main_model), sqlc.narg(main_context_length),
    NULLIF(sqlc.arg(subagent_model)::text, ''), sqlc.narg(subagent_context_length), NULLIF(sqlc.arg(compact_model)::text, ''), 1
WHERE sqlc.arg(expected_version)::bigint = 0
   OR EXISTS (SELECT 1 FROM user_model_settings WHERE user_id = sqlc.arg(user_id))
ON CONFLICT (user_id) DO UPDATE SET main_model = EXCLUDED.main_model,
    main_context_length = EXCLUDED.main_context_length, subagent_model = EXCLUDED.subagent_model,
    subagent_context_length = EXCLUDED.subagent_context_length, compact_model = EXCLUDED.compact_model,
    version = user_model_settings.version + 1
WHERE user_model_settings.version = sqlc.arg(expected_version)
RETURNING version;

-- name: GetRuntimeSettings :one
SELECT compact_at_tokens, reflection_idle_minutes, version
FROM model_settings WHERE singleton = true;

-- name: SaveRuntimeSettings :one
-- 保留旧的全局模型列供旧版回滚，当前运行时只读取阈值和反思时长。
INSERT INTO model_settings (main_model, compact_at_tokens, reflection_idle_minutes, version, updated_by)
SELECT 'openrouter/free', sqlc.arg(compact_at_tokens), sqlc.arg(reflection_idle_minutes), 1, '管理员'
WHERE sqlc.arg(expected_version)::bigint = 0
   OR EXISTS (SELECT 1 FROM model_settings WHERE singleton = true)
ON CONFLICT (singleton) DO UPDATE SET compact_at_tokens = EXCLUDED.compact_at_tokens,
    reflection_idle_minutes = EXCLUDED.reflection_idle_minutes,
    version = model_settings.version + 1, updated_at = now(), updated_by = EXCLUDED.updated_by
WHERE model_settings.version = sqlc.arg(expected_version)
RETURNING version;
