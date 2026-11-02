-- +goose Up
-- 删除 Subagent：每个用户只剩 Main 一个 Agent。先清掉 Subagent 上下文里的消息、步骤与调用记录，
-- 以及 Main 上下文里由 Subagent 发来的消息，再删各表的归属列与 subagents 表；本迁移不保留 Subagent 的历史内容。
DELETE FROM messages WHERE subagent_id IS NOT NULL OR sender_subagent_id IS NOT NULL OR sender = 'subagent';
DELETE FROM steps WHERE subagent_id IS NOT NULL;
DELETE FROM model_calls WHERE subagent_id IS NOT NULL;

DROP INDEX messages_agent_id_idx;
DROP INDEX messages_agent_sequence_idx;
DROP INDEX messages_summary_idx;
DROP INDEX messages_instructions_idx;
DROP INDEX model_calls_subagent_started_idx;
DROP INDEX model_calls_in_flight_idx;

ALTER TABLE messages DROP COLUMN sender_subagent_id, DROP COLUMN subagent_id;
ALTER TABLE steps DROP COLUMN subagent_id;
ALTER TABLE model_calls DROP COLUMN subagent_id;
DROP TABLE subagents;

ALTER TABLE messages DROP CONSTRAINT messages_sender_check;
ALTER TABLE messages ADD CONSTRAINT messages_sender_check CHECK (sender IN ('user', 'main', 'system'));

CREATE INDEX messages_agent_sequence_idx ON messages (user_id, sequence);
CREATE INDEX messages_summary_idx ON messages (user_id, sequence) WHERE is_summary;
CREATE INDEX messages_instructions_idx ON messages (user_id, id) WHERE content ->> 'name' = 'instructions';
CREATE INDEX model_calls_in_flight_idx ON model_calls (user_id) WHERE outcome = 'in_flight';

ALTER TABLE model_settings DROP COLUMN subagent_model, DROP COLUMN subagent_model_max_tokens, DROP COLUMN subagent_context_length;

-- +goose Down
-- 只恢复结构，不恢复已删除的 Subagent 记录。
ALTER TABLE model_settings
    ADD COLUMN subagent_model text CHECK (length(subagent_model) BETWEEN 1 AND 160),
    ADD COLUMN subagent_model_max_tokens integer CHECK (subagent_model_max_tokens > 0),
    ADD COLUMN subagent_context_length bigint CHECK (subagent_context_length > 0);

DROP INDEX model_calls_in_flight_idx;
DROP INDEX messages_instructions_idx;
DROP INDEX messages_summary_idx;
DROP INDEX messages_agent_sequence_idx;

ALTER TABLE messages DROP CONSTRAINT messages_sender_check;
ALTER TABLE messages ADD CONSTRAINT messages_sender_check CHECK (sender IN ('user', 'main', 'subagent', 'system'));

CREATE TABLE subagents (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status text NOT NULL CHECK (status IN ('running', 'idle', 'failed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz
);
CREATE INDEX subagents_user_id_idx ON subagents (user_id);

ALTER TABLE model_calls ADD COLUMN subagent_id uuid REFERENCES subagents (id) ON DELETE CASCADE;
ALTER TABLE steps ADD COLUMN subagent_id uuid REFERENCES subagents (id) ON DELETE CASCADE;
ALTER TABLE messages ADD COLUMN subagent_id uuid REFERENCES subagents (id) ON DELETE CASCADE,
    ADD COLUMN sender_subagent_id uuid REFERENCES subagents (id) ON DELETE CASCADE;

CREATE INDEX model_calls_in_flight_idx ON model_calls (user_id, subagent_id) WHERE outcome = 'in_flight';
CREATE INDEX model_calls_subagent_started_idx ON model_calls (subagent_id, started_at DESC, id DESC) WHERE subagent_id IS NOT NULL;
CREATE INDEX messages_instructions_idx ON messages (user_id, subagent_id, id) WHERE content ->> 'name' = 'instructions';
CREATE INDEX messages_summary_idx ON messages (user_id, subagent_id, sequence) WHERE is_summary;
CREATE INDEX messages_agent_sequence_idx ON messages (user_id, subagent_id, sequence);
CREATE INDEX messages_agent_id_idx ON messages (user_id, subagent_id, id);
