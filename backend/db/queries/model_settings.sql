-- 本文件是运行时模型设置的查询集合：读取、锁定并保存管理员在看板选择的模型配置（sqlc 据此生成代码）。

-- name: GetModelSettings :one
SELECT main_model, subagent_model, main_model_max_tokens, subagent_model_max_tokens, compact_after_hours, compact_at_tokens, main_context_length, subagent_context_length, version, updated_at, updated_by FROM model_settings WHERE singleton = true;

-- name: LockModelSettings :exec
SELECT pg_advisory_xact_lock(hashtextextended('admin:model-settings', 0));

-- name: SaveModelSettings :exec
INSERT INTO model_settings (main_model, subagent_model, main_model_max_tokens, subagent_model_max_tokens, compact_after_hours, compact_at_tokens, main_context_length, subagent_context_length, version, updated_by)
VALUES (sqlc.arg(main_model), sqlc.narg(subagent_model), sqlc.narg(main_model_max_tokens), sqlc.narg(subagent_model_max_tokens), sqlc.arg(compact_after_hours), sqlc.arg(compact_at_tokens), sqlc.narg(main_context_length), sqlc.narg(subagent_context_length), 1, sqlc.arg(updated_by))
ON CONFLICT (singleton) DO UPDATE SET main_model = EXCLUDED.main_model,
    subagent_model = EXCLUDED.subagent_model, main_model_max_tokens = EXCLUDED.main_model_max_tokens,
    subagent_model_max_tokens = EXCLUDED.subagent_model_max_tokens,
    compact_after_hours = EXCLUDED.compact_after_hours, compact_at_tokens = EXCLUDED.compact_at_tokens,
    main_context_length = EXCLUDED.main_context_length, subagent_context_length = EXCLUDED.subagent_context_length, version = model_settings.version + 1,
    updated_at = now(), updated_by = EXCLUDED.updated_by;
