-- 本迁移登记 Agent 实例和临时用户归属；新输入按归属定向通知，无归属的工作等待调度分配。

-- +goose Up
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

-- 输入提交后才投递通知；存活归属直接唤醒执行者，没有归属时只通知调度待分配用户。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION notify_main_input() RETURNS trigger LANGUAGE plpgsql AS $$
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

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION notify_main_input() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('main_input', json_build_object('user_id', NEW.user_id, 'sequence', NEW.sequence)::text);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TABLE agent_routes;
DROP TABLE agent_instances;
