-- +goose Up
ALTER TABLE messages ADD COLUMN visible boolean NOT NULL DEFAULT false;

-- 聊天界面过去靠 sender 与 role 推断可见性；改为显式记录：Main 的真人消息与助手消息可见，其余上下文不可见。
UPDATE messages SET visible = true
WHERE subagent_id IS NULL AND (sender = 'user' OR role = 'assistant');

-- 旧的运行时 system 消息改为不可见 user 消息，并用 name 标明用途，供运行时继续识别。
UPDATE messages
SET role = 'user',
    content = content || jsonb_build_object(
        'role', 'user',
        'name', CASE
            WHEN content ->> 'content' LIKE 'Runtime progress check:%' THEN 'progress_reminder'
            WHEN content ->> 'content' LIKE 'Runtime delivery check:%' THEN 'delivery_correction'
            ELSE 'runtime'
        END
    )
WHERE role = 'system';

ALTER TABLE messages DROP CONSTRAINT messages_role_check;
ALTER TABLE messages ADD CONSTRAINT messages_role_check CHECK (role IN ('user', 'assistant', 'tool'));

CREATE INDEX messages_instructions_idx ON messages (user_id, subagent_id, id)
WHERE content ->> 'name' = 'instructions';

-- +goose Down
DROP INDEX messages_instructions_idx;

ALTER TABLE messages DROP CONSTRAINT messages_role_check;
ALTER TABLE messages ADD CONSTRAINT messages_role_check CHECK (role IN ('system', 'user', 'assistant', 'tool'));

UPDATE messages
SET role = 'system',
    content = (content - 'name') || jsonb_build_object('role', 'system')
WHERE role = 'user' AND content ->> 'name' IN ('progress_reminder', 'delivery_correction', 'runtime');

ALTER TABLE messages DROP COLUMN visible;
