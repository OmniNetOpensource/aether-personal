-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    email text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE subagents (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status text NOT NULL CHECK (status IN ('running', 'idle', 'failed')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX subagents_user_id_idx ON subagents (user_id);

CREATE TABLE steps (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    subagent_id uuid REFERENCES subagents (id) ON DELETE CASCADE,
    model text NOT NULL,
    input_tokens integer,
    output_tokens integer,
    cost numeric,
    error text,
    started_at timestamptz NOT NULL,
    finished_at timestamptz
);

CREATE SEQUENCE message_sequence;

CREATE TABLE messages (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    subagent_id uuid REFERENCES subagents (id) ON DELETE CASCADE,
    sequence bigint UNIQUE DEFAULT nextval('message_sequence'),
    role text NOT NULL CHECK (role IN ('system', 'user', 'assistant', 'tool')),
    content jsonb NOT NULL,
    sender text CHECK (sender IN ('user', 'main', 'subagent', 'system')),
    sender_subagent_id uuid REFERENCES subagents (id) ON DELETE CASCADE,
    step_id uuid REFERENCES steps (id) ON DELETE CASCADE,
    is_summary boolean NOT NULL DEFAULT false,
    embedding vector(1536),
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER SEQUENCE message_sequence OWNED BY messages.sequence;

CREATE INDEX messages_agent_sequence_idx ON messages (user_id, subagent_id, sequence);
CREATE INDEX messages_summary_idx ON messages (user_id, subagent_id, sequence) WHERE is_summary;
CREATE INDEX messages_embedding_pending_idx ON messages (id) WHERE embedding IS NULL;
CREATE INDEX messages_embedding_idx ON messages USING hnsw (embedding vector_cosine_ops);

-- +goose Down
DROP TABLE messages;
DROP TABLE steps;
DROP TABLE subagents;
DROP TABLE users;
DROP EXTENSION vector;
