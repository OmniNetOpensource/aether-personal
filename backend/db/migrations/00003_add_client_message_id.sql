-- +goose Up
ALTER TABLE messages ADD COLUMN client_message_id uuid;
ALTER TABLE messages ADD CONSTRAINT messages_user_client_message_id_key UNIQUE (user_id, client_message_id);
CREATE INDEX messages_agent_id_idx ON messages (user_id, subagent_id, id);

-- +goose Down
DROP INDEX messages_agent_id_idx;
ALTER TABLE messages DROP CONSTRAINT messages_user_client_message_id_key;
ALTER TABLE messages DROP COLUMN client_message_id;
