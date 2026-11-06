-- 本迁移移除多实例目录与输入调度触发器；单进程 Manager 在消息提交后直接接收唤醒，业务待办仍由 main_work 推导。

-- +goose Up
DROP TRIGGER main_input_insert ON messages;
DROP FUNCTION notify_main_input();
DROP TABLE agent_routes;
DROP TABLE agent_instances;

-- +goose Down
CREATE TABLE agent_instances (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    capacity integer NOT NULL CHECK (capacity > 0),
    heartbeat_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    accepting_work boolean NOT NULL DEFAULT true
);
CREATE TABLE agent_routes (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    instance_id uuid NOT NULL REFERENCES agent_instances(id) ON DELETE CASCADE,
    assignment_id uuid NOT NULL DEFAULT uuidv7(),
    assigned_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX agent_routes_instance_idx ON agent_routes(instance_id);

-- 降级恢复旧版按实例归属投递通知的能力，已删除的临时归属由旧版调度器重建。
-- +goose StatementBegin
CREATE FUNCTION notify_main_input() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE route agent_routes;
BEGIN
    SELECT r.* INTO route FROM agent_routes AS r JOIN agent_instances AS i ON i.id = r.instance_id
    WHERE r.user_id = NEW.user_id AND i.heartbeat_at > clock_timestamp() - interval '20 seconds';
    IF FOUND THEN
        PERFORM pg_notify('agent_instance_' || replace(route.instance_id::text, '-', ''),
            json_build_object('user_id', NEW.user_id, 'assignment_id', route.assignment_id, 'sequence', NEW.sequence)::text);
    ELSE
        PERFORM pg_notify('agent_pending', NEW.user_id::text);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER main_input_insert AFTER INSERT ON messages
FOR EACH ROW WHEN (NEW.role = 'user' AND NOT NEW.is_summary
    AND NEW.content ->> 'name' IS DISTINCT FROM 'memory_context') EXECUTE FUNCTION notify_main_input();
