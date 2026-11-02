-- +goose Up
-- 聊天展示按原来的行位置排序，占位回复完成时另占变更序号，已越过该行的设备仍能补到正文。
ALTER TABLE messages ADD COLUMN chat_updated_sequence bigint;
CREATE INDEX messages_chat_changes_idx ON messages (user_id, (coalesce(chat_updated_sequence, sequence))) WHERE visible;

-- +goose StatementBegin
CREATE FUNCTION assign_visible_chat_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.chat_updated_sequence := nextval('message_sequence');
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER messages_became_visible BEFORE UPDATE OF visible ON messages
    FOR EACH ROW WHEN (NOT OLD.visible AND NEW.visible) EXECUTE FUNCTION assign_visible_chat_change();

-- +goose Down
DROP TRIGGER messages_became_visible ON messages;
DROP FUNCTION assign_visible_chat_change();
DROP INDEX messages_chat_changes_idx;
ALTER TABLE messages DROP COLUMN chat_updated_sequence;
