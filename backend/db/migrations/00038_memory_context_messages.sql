-- 本迁移让已保存的根快照消息留在上下文中，不作为新的外部输入投递通知。

-- +goose Up
DROP TRIGGER main_input_insert ON messages;
CREATE TRIGGER main_input_insert AFTER INSERT ON messages
FOR EACH ROW WHEN (NEW.role = 'user' AND NOT NEW.is_summary
    AND NEW.content ->> 'name' IS DISTINCT FROM 'memory_context') EXECUTE FUNCTION notify_main_input();

-- +goose Down
DROP TRIGGER main_input_insert ON messages;
CREATE TRIGGER main_input_insert AFTER INSERT ON messages
FOR EACH ROW WHEN (NEW.role = 'user' AND NOT NEW.is_summary) EXECUTE FUNCTION notify_main_input();
