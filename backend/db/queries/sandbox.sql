-- name: EnsureUserSandbox :one
INSERT INTO user_sandboxes (user_id)
VALUES (sqlc.arg(user_id))
ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id
RETURNING *;

-- name: GetUserSandbox :one
SELECT * FROM user_sandboxes WHERE user_id = sqlc.arg(user_id);

-- name: MarkSandboxCreating :exec
UPDATE user_sandboxes SET provision_state = 'creating', last_error = NULL
WHERE user_id = sqlc.arg(user_id);

-- name: SaveUserSandbox :exec
UPDATE user_sandboxes
SET provider_id = sqlc.arg(provider_id)::text, provision_state = 'ready',
    last_error = NULL, last_activity_at = now()
WHERE user_id = sqlc.arg(user_id);

-- name: SetSandboxProvisionError :exec
UPDATE user_sandboxes
SET provision_state = sqlc.arg(state)::text, last_error = sqlc.narg(error)::text
WHERE user_id = sqlc.arg(user_id);

-- name: TouchUserSandbox :exec
UPDATE user_sandboxes SET last_activity_at = now() WHERE user_id = sqlc.arg(user_id);

-- name: GetStepModelReply :one
SELECT model_reply FROM steps WHERE id = sqlc.arg(step_id);

-- name: SaveStepModelReply :exec
UPDATE steps SET model_reply = sqlc.arg(payload)::jsonb
WHERE id = sqlc.arg(step_id) AND model_reply IS NULL;

-- name: DeferSandboxStep :exec
UPDATE steps SET resume_after = now() + interval '2 seconds' WHERE id = sqlc.arg(step_id);

-- name: ClearSandboxStepDeferral :exec
UPDATE steps SET resume_after = NULL WHERE id = sqlc.arg(step_id);

-- name: EnsureSandboxOperation :one
INSERT INTO sandbox_operations (user_id, step_id, tool_call_id, tool_name, args)
VALUES (sqlc.arg(user_id), sqlc.arg(step_id), sqlc.arg(tool_call_id), sqlc.arg(tool_name), sqlc.arg(args)::jsonb)
ON CONFLICT (step_id, tool_call_id) DO UPDATE SET tool_call_id = EXCLUDED.tool_call_id
RETURNING *;

-- name: MarkSandboxOperationSubmitting :exec
UPDATE sandbox_operations SET state = 'submitting', updated_at = now()
WHERE id = sqlc.arg(id) AND state = 'planned';

-- name: SetSandboxOperationCommand :exec
UPDATE sandbox_operations
SET state = 'running', provider_command_id = sqlc.arg(command_id)::text,
    result = sqlc.narg(result)::jsonb, updated_at = now()
WHERE id = sqlc.arg(id);

-- name: CompleteSandboxOperation :exec
UPDATE sandbox_operations
SET state = sqlc.arg(state)::text, result = sqlc.arg(result)::jsonb, updated_at = now()
WHERE id = sqlc.arg(id);

-- name: ListActiveSandboxOperations :many
SELECT * FROM sandbox_operations
WHERE user_id = sqlc.arg(user_id) AND state IN ('running', 'submitting', 'unknown')
ORDER BY created_at, id;

-- name: GetSandboxCommandOperation :one
SELECT * FROM sandbox_operations
WHERE user_id = sqlc.arg(user_id) AND provider_command_id = sqlc.arg(command_id)::text
ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ListIdleUserSandboxes :many
SELECT sandbox.* FROM user_sandboxes AS sandbox
WHERE sandbox.provider_id IS NOT NULL AND sandbox.provision_state = 'ready'
    AND sandbox.last_activity_at < sqlc.arg(idle_before)::timestamptz
    AND NOT EXISTS (SELECT 1 FROM steps AS step WHERE step.user_id = sandbox.user_id AND step.finished_at IS NULL)
    AND NOT EXISTS (SELECT 1 FROM messages AS message WHERE message.user_id = sandbox.user_id AND message.sequence IS NULL)
    AND NOT EXISTS (SELECT 1 FROM subagents AS subagent WHERE subagent.user_id = sandbox.user_id AND subagent.status = 'running')
ORDER BY sandbox.last_activity_at, sandbox.user_id;

-- name: HasSandboxAgentWork :one
SELECT EXISTS (
    SELECT 1 FROM steps AS step WHERE step.user_id = sqlc.arg(user_id)::uuid AND step.finished_at IS NULL
    UNION ALL
    SELECT 1 FROM messages AS message WHERE message.user_id = sqlc.arg(user_id)::uuid AND message.sequence IS NULL
    UNION ALL
    SELECT 1 FROM subagents AS subagent WHERE subagent.user_id = sqlc.arg(user_id)::uuid AND subagent.status = 'running'
);
