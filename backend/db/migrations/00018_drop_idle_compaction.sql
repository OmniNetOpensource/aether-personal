-- +goose Up
ALTER TABLE messages DROP CONSTRAINT messages_compact_reply;
ALTER TABLE messages DROP COLUMN compact_reply_sequence;
ALTER TABLE model_settings DROP COLUMN compact_after_hours;

-- +goose Down
ALTER TABLE model_settings ADD COLUMN compact_after_hours integer NOT NULL DEFAULT 6 CHECK (compact_after_hours BETWEEN 1 AND 8760);
ALTER TABLE messages ADD COLUMN compact_reply_sequence bigint;
ALTER TABLE messages ADD CONSTRAINT messages_compact_reply CHECK (compact_reply_sequence IS NULL OR is_summary);
