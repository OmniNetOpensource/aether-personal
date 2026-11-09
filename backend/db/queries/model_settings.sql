-- 本文件是运行时模型设置的查询集合：读取、锁定并保存管理员在看板选择的模型配置与三种闲置后台工作的时长（sqlc 据此生成代码）。

-- name: GetModelSettings :one
SELECT main_model, main_model_max_tokens, compact_model, compact_model_max_tokens,
    compact_at_tokens, main_context_length, compact_context_length,
    memory_update_idle_minutes, recent_followup_idle_minutes, unknown_unknowns_idle_minutes, version, updated_at, updated_by
FROM model_settings WHERE singleton = true;

-- name: LockModelSettings :exec
SELECT pg_advisory_xact_lock(hashtextextended('admin:model-settings', 0));

-- name: SaveModelSettings :exec
INSERT INTO model_settings (main_model, main_model_max_tokens, compact_model, compact_model_max_tokens,
    compact_at_tokens, main_context_length, compact_context_length,
    memory_update_idle_minutes, recent_followup_idle_minutes, unknown_unknowns_idle_minutes, version, updated_by)
VALUES (sqlc.arg(main_model), sqlc.narg(main_model_max_tokens), sqlc.narg(compact_model), sqlc.narg(compact_model_max_tokens),
    sqlc.arg(compact_at_tokens), sqlc.narg(main_context_length), sqlc.narg(compact_context_length),
    sqlc.arg(memory_update_idle_minutes), sqlc.arg(recent_followup_idle_minutes), sqlc.arg(unknown_unknowns_idle_minutes), 1, sqlc.arg(updated_by))
ON CONFLICT (singleton) DO UPDATE SET main_model = EXCLUDED.main_model,
    main_model_max_tokens = EXCLUDED.main_model_max_tokens,
    compact_model = EXCLUDED.compact_model,
    compact_model_max_tokens = EXCLUDED.compact_model_max_tokens,
    compact_at_tokens = EXCLUDED.compact_at_tokens,
    main_context_length = EXCLUDED.main_context_length,
    compact_context_length = EXCLUDED.compact_context_length,
    memory_update_idle_minutes = EXCLUDED.memory_update_idle_minutes,
    recent_followup_idle_minutes = EXCLUDED.recent_followup_idle_minutes,
    unknown_unknowns_idle_minutes = EXCLUDED.unknown_unknowns_idle_minutes,
    version = model_settings.version + 1,
    updated_at = now(), updated_by = EXCLUDED.updated_by;
