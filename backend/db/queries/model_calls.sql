-- name: StartModelCall :one
WITH step AS (
 UPDATE steps AS source SET model = sqlc.arg(requested_model) WHERE source.id = sqlc.arg(step_id)
 RETURNING source.id, source.user_id, source.subagent_id
)
INSERT INTO model_calls (step_id, user_id, subagent_id, requested_model, request_body, purpose, compact_reason, context_tokens_before, summary_through_sequence)
SELECT step.id, step.user_id, step.subagent_id, sqlc.arg(requested_model), sqlc.narg(request_body)::jsonb, sqlc.arg(purpose), sqlc.narg(compact_reason), sqlc.narg(context_tokens_before), sqlc.narg(summary_through_sequence) FROM step
RETURNING id;

-- name: FinishModelCall :execrows
UPDATE model_calls SET
    outcome = sqlc.arg(outcome),
    http_status = sqlc.narg(http_status),
    finished_at = now(),
    duration_ms = sqlc.narg(duration_ms),
    response_model = sqlc.narg(response_model),
    provider_generation_id = sqlc.narg(provider_generation_id),
    input_tokens = sqlc.narg(input_tokens),
    output_tokens = sqlc.narg(output_tokens),
    cost = sqlc.narg(cost),
    tool_names = sqlc.arg(tool_names)::text[],
    failure_kind = sqlc.narg(failure_kind),
    response_body = sqlc.narg(response_body),
    response_truncated = sqlc.arg(response_truncated)
WHERE id = sqlc.arg(id) AND outcome = 'in_flight';

-- name: InterruptModelCalls :exec
UPDATE model_calls SET outcome = 'interrupted', finished_at = now(), failure_kind = 'interrupted'
WHERE user_id = sqlc.arg(user_id)
    AND subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid
    AND purpose <> 'recap' AND outcome = 'in_flight';

-- name: HasAgentExecutionWork :one
SELECT EXISTS (
 SELECT 1 FROM messages AS m WHERE m.user_id = sqlc.arg(user_id)
  AND m.subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid AND m.sequence IS NULL
 UNION ALL
 SELECT 1 FROM steps AS s WHERE s.user_id = sqlc.arg(user_id)
  AND s.subagent_id IS NOT DISTINCT FROM sqlc.narg(subagent_id)::uuid AND s.finished_at IS NULL
 UNION ALL
 SELECT 1 FROM subagents AS a WHERE a.user_id = sqlc.arg(user_id)
  AND a.id = sqlc.narg(subagent_id)::uuid AND a.status = 'running'
) AS has_work;
