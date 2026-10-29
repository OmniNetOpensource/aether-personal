-- 本文件是管理看板的查询集合：汇总运行指标，并分页列出用户、Agent 与模型调用，供看板接口读取（sqlc 据此生成代码）。

-- name: AdminSnapshot :one
WITH calls AS (
 SELECT c.* FROM model_calls c JOIN users u ON u.id = c.user_id WHERE c.started_at >= sqlc.arg(from_at)::timestamptz AND c.started_at < sqlc.arg(to_at)::timestamptz
  AND (sqlc.narg(user_id)::uuid IS NULL OR c.user_id = sqlc.narg(user_id)::uuid)
  AND (sqlc.arg(agent_kind)::text = 'all' OR (sqlc.arg(agent_kind)::text = 'main' AND c.subagent_id IS NULL AND c.purpose <> 'recap')
       OR (sqlc.arg(agent_kind)::text = 'subagent' AND c.subagent_id = sqlc.narg(agent_id)::uuid)
       OR (sqlc.arg(agent_kind)::text = 'recap' AND c.purpose = 'recap'))
  AND (sqlc.narg(model)::text IS NULL OR c.requested_model = sqlc.narg(model)::text)
  AND (sqlc.narg(outcome)::text IS NULL OR c.outcome = sqlc.narg(outcome)::text)
  AND (sqlc.arg(search)::text = '' OR strpos(lower(u.email), lower(sqlc.arg(search)::text)) > 0)
), legacy AS (
 SELECT s.* FROM steps s JOIN users u ON u.id = s.user_id WHERE s.started_at >= sqlc.arg(from_at)::timestamptz AND s.started_at < sqlc.arg(to_at)::timestamptz
  AND (sqlc.narg(user_id)::uuid IS NULL OR s.user_id = sqlc.narg(user_id)::uuid)
  AND (sqlc.arg(agent_kind)::text = 'all' OR (sqlc.arg(agent_kind)::text = 'main' AND s.subagent_id IS NULL)
       OR (sqlc.arg(agent_kind)::text = 'subagent' AND s.subagent_id = sqlc.narg(agent_id)::uuid))
  AND (sqlc.narg(model)::text IS NULL OR s.model = sqlc.narg(model)::text)
  AND sqlc.narg(outcome)::text IS NULL
  AND (sqlc.arg(search)::text = '' OR strpos(lower(u.email), lower(sqlc.arg(search)::text)) > 0)
  AND (s.finished_at IS NOT NULL OR s.model_reply IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM model_calls mc WHERE mc.step_id = s.id)
), buckets AS (
 SELECT generate_series(date_trunc(sqlc.arg(bucket)::text, sqlc.arg(from_at)::timestamptz, 'UTC'),
   sqlc.arg(to_at)::timestamptz - interval '1 microsecond',
   CASE sqlc.arg(bucket)::text WHEN 'hour' THEN interval '1 hour' ELSE interval '1 day' END) AS at
), counts AS (
 SELECT date_trunc(sqlc.arg(bucket)::text, c.started_at, 'UTC') AS at, count(*) AS attempts,
   count(*) FILTER (WHERE c.outcome = 'succeeded') AS succeeded,
   count(*) FILTER (WHERE c.outcome IN ('http_error','transport_error','invalid_response')) AS failed FROM calls c GROUP BY 1
), models AS (
 SELECT c.requested_model AS model, count(*) AS attempts, coalesce(sum(c.cost),0)::text AS cost_known_usd
 FROM calls c GROUP BY c.requested_model ORDER BY count(*) DESC, c.requested_model LIMIT 100
), tool_counts AS (
 SELECT CASE WHEN tool.name IN ('send_message','assign_subagents','search','fetch_url') THEN tool.name ELSE 'other' END AS name, count(*) AS count
 FROM calls c CROSS JOIN LATERAL unnest(c.tool_names) AS tool(name) GROUP BY 1
)
SELECT jsonb_build_object(
 'generated_at', now(),
 'range', jsonb_build_object('from',sqlc.arg(from_at)::timestamptz,'to',sqlc.arg(to_at)::timestamptz,'bucket',sqlc.arg(bucket)::text),
 'coverage', jsonb_build_object('first_observed_at',(SELECT min(started_at) FROM model_calls),'legacy_steps',(SELECT count(*) FROM legacy)),
 'metrics', (SELECT jsonb_build_object('attempts',count(*),'succeeded',count(*) FILTER (WHERE c.outcome='succeeded'),
   'failed',count(*) FILTER (WHERE c.outcome IN ('http_error','transport_error','invalid_response')),'in_flight',count(*) FILTER (WHERE c.outcome='in_flight'),
   'interrupted',count(*) FILTER (WHERE c.outcome='interrupted'),
   'input_tokens',coalesce(sum(c.input_tokens),0),'output_tokens',coalesce(sum(c.output_tokens),0),
   'input_unknown',count(*) FILTER (WHERE c.input_tokens IS NULL),'output_unknown',count(*) FILTER (WHERE c.output_tokens IS NULL),
   'cost_known_usd',coalesce(sum(c.cost),0)::text,'cost_unknown',count(*) FILTER (WHERE c.cost IS NULL),
   'mean_duration_ms',avg(c.duration_ms)::float8,'duration_samples',count(c.duration_ms)) FROM calls c),
 'trend', (SELECT coalesce(jsonb_agg(jsonb_build_object('at',b.at,'attempts',coalesce(c.attempts,0),
   'succeeded',coalesce(c.succeeded,0),'failed',coalesce(c.failed,0)) ORDER BY b.at),'[]'::jsonb) FROM buckets b LEFT JOIN counts c ON c.at=b.at),
 'models',(SELECT coalesce(jsonb_agg(to_jsonb(models)),'[]'::jsonb) FROM models),
 'tools',(SELECT coalesce(jsonb_agg(to_jsonb(tool_counts) ORDER BY count DESC,name),'[]'::jsonb) FROM tool_counts),
 'legacy',(SELECT jsonb_build_object('steps',count(*),'failed_steps',count(*) FILTER (WHERE error IS NOT NULL),
   'recorded_input_tokens',coalesce(sum(input_tokens),0),'recorded_output_tokens',coalesce(sum(output_tokens),0),
   'recorded_cost_usd',coalesce(sum(cost),0)::text) FROM legacy)
)::jsonb AS payload;

-- name: ListAdminUsers :many
WITH page AS (
 SELECT u.* FROM users u WHERE (sqlc.narg(user_id)::uuid IS NULL OR u.id=sqlc.narg(user_id)::uuid)
 AND (sqlc.arg(search)::text='' OR strpos(lower(u.email),lower(sqlc.arg(search)::text))>0)
 AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (u.created_at,u.id)<(sqlc.narg(cursor_at)::timestamptz,sqlc.narg(cursor_id)::uuid))
 ORDER BY u.created_at DESC,u.id DESC LIMIT sqlc.arg(page_limit)::integer
)
SELECT u.id,u.created_at,jsonb_build_object('id',u.id,'email',u.email,'created_at',u.created_at,
 'last_activity_at',(SELECT max(s.started_at) FROM steps s WHERE s.user_id=u.id),
 'main_active',EXISTS(SELECT 1 FROM steps s WHERE s.user_id=u.id AND s.subagent_id IS NULL AND s.finished_at IS NULL)
   OR EXISTS(SELECT 1 FROM messages m WHERE m.user_id=u.id AND m.subagent_id IS NULL AND m.sequence IS NULL),
 'active_subagents',(SELECT count(*) FROM subagents a WHERE a.user_id=u.id AND a.status='running'),
 'total_subagents',(SELECT count(*) FROM subagents a WHERE a.user_id=u.id),
 'attempts',stats.attempts,'failed',stats.failed,'input_tokens',stats.input_tokens,'output_tokens',stats.output_tokens,
 'cost_known_usd',stats.cost_known_usd,'cost_unknown',stats.cost_unknown,
 'legacy_steps',(SELECT count(*) FROM steps s WHERE s.user_id=u.id AND s.started_at >= sqlc.arg(from_at)::timestamptz AND s.started_at < sqlc.arg(to_at)::timestamptz
  AND (sqlc.narg(user_id)::uuid IS NULL OR s.user_id = sqlc.narg(user_id)::uuid)
  AND (sqlc.arg(agent_kind)::text = 'all' OR (sqlc.arg(agent_kind)::text = 'main' AND s.subagent_id IS NULL)
       OR (sqlc.arg(agent_kind)::text = 'subagent' AND s.subagent_id = sqlc.narg(agent_id)::uuid))
  AND (sqlc.narg(model)::text IS NULL OR s.model = sqlc.narg(model)::text)
  AND sqlc.narg(outcome)::text IS NULL
  AND (sqlc.arg(search)::text = '' OR strpos(lower(u.email), lower(sqlc.arg(search)::text)) > 0)
  AND (s.finished_at IS NOT NULL OR s.model_reply IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM model_calls mc WHERE mc.step_id = s.id))
)::jsonb AS payload
FROM page u CROSS JOIN LATERAL (
 SELECT count(*) AS attempts,count(*) FILTER(WHERE c.outcome IN ('http_error','transport_error','invalid_response')) AS failed,
 coalesce(sum(c.input_tokens),0) AS input_tokens,coalesce(sum(c.output_tokens),0) AS output_tokens,
 coalesce(sum(c.cost),0)::text AS cost_known_usd,count(*) FILTER(WHERE c.cost IS NULL) AS cost_unknown
 FROM model_calls c WHERE c.user_id=u.id AND c.started_at >= sqlc.arg(from_at)::timestamptz AND c.started_at < sqlc.arg(to_at)::timestamptz
  AND (sqlc.narg(user_id)::uuid IS NULL OR c.user_id = sqlc.narg(user_id)::uuid)
  AND (sqlc.arg(agent_kind)::text = 'all' OR (sqlc.arg(agent_kind)::text = 'main' AND c.subagent_id IS NULL AND c.purpose <> 'recap')
       OR (sqlc.arg(agent_kind)::text = 'subagent' AND c.subagent_id = sqlc.narg(agent_id)::uuid)
       OR (sqlc.arg(agent_kind)::text = 'recap' AND c.purpose = 'recap'))
  AND (sqlc.narg(model)::text IS NULL OR c.requested_model = sqlc.narg(model)::text)
  AND (sqlc.narg(outcome)::text IS NULL OR c.outcome = sqlc.narg(outcome)::text)
  AND (sqlc.arg(search)::text = '' OR strpos(lower(u.email), lower(sqlc.arg(search)::text)) > 0)
) stats ORDER BY u.created_at DESC,u.id DESC;

-- name: ListAdminAgents :many
WITH agents AS (
 SELECT NULL::uuid AS id,u.id AS user_id,u.email,u.created_at,
   CASE WHEN EXISTS(SELECT 1 FROM steps s WHERE s.user_id=u.id AND s.subagent_id IS NULL AND s.finished_at IS NULL)
     OR EXISTS(SELECT 1 FROM messages m WHERE m.user_id=u.id AND m.subagent_id IS NULL AND m.sequence IS NULL)
     THEN 'processing' WHEN (SELECT s.error IS NOT NULL FROM steps s WHERE s.user_id=u.id AND s.subagent_id IS NULL ORDER BY s.started_at DESC,s.id DESC LIMIT 1)
     THEN 'failed' ELSE 'idle' END AS status
 FROM users u WHERE u.id=sqlc.arg(selected_user_id)::uuid
 UNION ALL
 SELECT a.id,a.user_id,u.email,a.created_at,CASE a.status WHEN 'running' THEN 'processing' ELSE a.status END
 FROM subagents a JOIN users u ON u.id=a.user_id WHERE a.user_id=sqlc.arg(selected_user_id)::uuid
), page AS (
 SELECT a.*,coalesce(a.id,'00000000-0000-0000-0000-000000000000'::uuid) AS cursor_id FROM agents a
 WHERE (sqlc.arg(agent_kind)::text='all' OR (sqlc.arg(agent_kind)::text='main' AND a.id IS NULL)
    OR (sqlc.arg(agent_kind)::text='subagent' AND a.id=sqlc.narg(agent_id)::uuid))
 AND (sqlc.arg(search)::text='' OR strpos(lower(a.email),lower(sqlc.arg(search)::text))>0)
 AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (a.created_at,coalesce(a.id,'00000000-0000-0000-0000-000000000000'::uuid))
    <(sqlc.narg(cursor_at)::timestamptz,sqlc.narg(cursor_id)::uuid))
 ORDER BY a.created_at DESC,cursor_id DESC LIMIT sqlc.arg(page_limit)::integer
)
SELECT a.cursor_id AS id,a.created_at,jsonb_build_object('id',a.id,'kind',CASE WHEN a.id IS NULL THEN 'main' ELSE 'subagent' END,
 'user_id',a.user_id,'email',a.email,'status',a.status,'created_at',a.created_at,
 'last_activity_at',(SELECT max(s.started_at) FROM steps s WHERE s.user_id=a.user_id AND s.subagent_id IS NOT DISTINCT FROM a.id),
 'attempts',stats.attempts,'failed',stats.failed,'input_tokens',stats.input_tokens,'output_tokens',stats.output_tokens,
 'cost_known_usd',stats.cost_known_usd,'cost_unknown',stats.cost_unknown,
 'legacy_steps',(SELECT count(*) FROM steps s JOIN users u ON u.id=s.user_id WHERE s.user_id=a.user_id AND s.subagent_id IS NOT DISTINCT FROM a.id AND s.started_at >= sqlc.arg(from_at)::timestamptz AND s.started_at < sqlc.arg(to_at)::timestamptz
  AND (sqlc.narg(user_id)::uuid IS NULL OR s.user_id = sqlc.narg(user_id)::uuid)
  AND (sqlc.arg(agent_kind)::text = 'all' OR (sqlc.arg(agent_kind)::text = 'main' AND s.subagent_id IS NULL)
       OR (sqlc.arg(agent_kind)::text = 'subagent' AND s.subagent_id = sqlc.narg(agent_id)::uuid))
  AND (sqlc.narg(model)::text IS NULL OR s.model = sqlc.narg(model)::text)
  AND sqlc.narg(outcome)::text IS NULL
  AND (sqlc.arg(search)::text = '' OR strpos(lower(u.email), lower(sqlc.arg(search)::text)) > 0)
  AND (s.finished_at IS NOT NULL OR s.model_reply IS NOT NULL)
  AND NOT EXISTS (SELECT 1 FROM model_calls mc WHERE mc.step_id = s.id))
)::jsonb AS payload FROM page a CROSS JOIN LATERAL (
 SELECT count(*) AS attempts,count(*) FILTER(WHERE c.outcome IN ('http_error','transport_error','invalid_response')) AS failed,
 coalesce(sum(c.input_tokens),0) AS input_tokens,coalesce(sum(c.output_tokens),0) AS output_tokens,
 coalesce(sum(c.cost),0)::text AS cost_known_usd,count(*) FILTER(WHERE c.cost IS NULL) AS cost_unknown
 FROM model_calls c JOIN users u ON u.id=c.user_id WHERE c.user_id=a.user_id AND c.purpose <> 'recap' AND c.subagent_id IS NOT DISTINCT FROM a.id AND c.started_at >= sqlc.arg(from_at)::timestamptz AND c.started_at < sqlc.arg(to_at)::timestamptz
  AND (sqlc.narg(user_id)::uuid IS NULL OR c.user_id = sqlc.narg(user_id)::uuid)
  AND (sqlc.arg(agent_kind)::text = 'all' OR (sqlc.arg(agent_kind)::text = 'main' AND c.subagent_id IS NULL AND c.purpose <> 'recap')
       OR (sqlc.arg(agent_kind)::text = 'subagent' AND c.subagent_id = sqlc.narg(agent_id)::uuid)
       OR (sqlc.arg(agent_kind)::text = 'recap' AND c.purpose = 'recap'))
  AND (sqlc.narg(model)::text IS NULL OR c.requested_model = sqlc.narg(model)::text)
  AND (sqlc.narg(outcome)::text IS NULL OR c.outcome = sqlc.narg(outcome)::text)
  AND (sqlc.arg(search)::text = '' OR strpos(lower(u.email), lower(sqlc.arg(search)::text)) > 0)
) stats ORDER BY a.created_at DESC,a.cursor_id DESC;

-- name: ListAdminCalls :many
SELECT c.id,c.started_at,jsonb_build_object('id',c.id,'step_id',c.step_id,'user_id',c.user_id,
 'agent_id',c.subagent_id,'email',u.email,'requested_model',c.requested_model,'response_model',c.response_model,
 'provider_generation_id',c.provider_generation_id,'outcome',c.outcome,'http_status',c.http_status,
 'purpose',c.purpose,'compact_reason',c.compact_reason,'context_tokens_before',c.context_tokens_before,
 'context_tokens_after',c.context_tokens_after,'summary_through_sequence',c.summary_through_sequence,
 'failure_kind',c.failure_kind,'started_at',c.started_at,'finished_at',c.finished_at,'duration_ms',c.duration_ms,
 'input_tokens',c.input_tokens,'output_tokens',c.output_tokens,'cost',c.cost::text,
 'tool_names',(SELECT coalesce(jsonb_agg(CASE WHEN name IN ('send_message','assign_subagents','search','fetch_url') THEN name ELSE 'other' END),'[]'::jsonb) FROM unnest(c.tool_names) AS tool(name))
)::jsonb AS payload FROM model_calls c JOIN users u ON u.id=c.user_id WHERE c.started_at >= sqlc.arg(from_at)::timestamptz AND c.started_at < sqlc.arg(to_at)::timestamptz
  AND (sqlc.narg(user_id)::uuid IS NULL OR c.user_id = sqlc.narg(user_id)::uuid)
  AND (sqlc.arg(agent_kind)::text = 'all' OR (sqlc.arg(agent_kind)::text = 'main' AND c.subagent_id IS NULL AND c.purpose <> 'recap')
       OR (sqlc.arg(agent_kind)::text = 'subagent' AND c.subagent_id = sqlc.narg(agent_id)::uuid)
       OR (sqlc.arg(agent_kind)::text = 'recap' AND c.purpose = 'recap'))
  AND (sqlc.narg(model)::text IS NULL OR c.requested_model = sqlc.narg(model)::text)
  AND (sqlc.narg(outcome)::text IS NULL OR c.outcome = sqlc.narg(outcome)::text)
  AND (sqlc.arg(search)::text = '' OR strpos(lower(u.email), lower(sqlc.arg(search)::text)) > 0)
 AND (sqlc.narg(step_id)::uuid IS NULL OR c.step_id=sqlc.narg(step_id)::uuid)
 AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (c.started_at,c.id)<(sqlc.narg(cursor_at)::timestamptz,sqlc.narg(cursor_id)::uuid))
 ORDER BY c.started_at DESC,c.id DESC LIMIT sqlc.arg(page_limit)::integer;

-- name: GetAdminCallDetail :one
SELECT request_body, response_body, response_truncated FROM model_calls WHERE id = $1;
