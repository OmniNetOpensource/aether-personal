-- 本迁移扩展长期工作区记录，记住已同步进 /mnt/workspace/uploads 的聊天附件序号，并在聊天搜索中纳入附件文件名。

-- +goose Up
ALTER TABLE sandbox_workspaces ADD COLUMN synced_through_sequence bigint NOT NULL DEFAULT 0;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION chat_message_text(content jsonb, role text) RETURNS text
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    extracted text := '';
    part jsonb;
BEGIN
    IF role IN ('user', 'tool') AND jsonb_typeof(content -> 'parts') = 'array' THEN
        FOR part IN SELECT value FROM jsonb_array_elements(content -> 'parts') LOOP
            IF part ->> 'type' = 'text' THEN
                extracted := extracted || coalesce(part ->> 'text', '') || E'\n';
            ELSIF part ->> 'type' = 'quote' THEN
                extracted := extracted || coalesce(part -> 'quote' ->> 'text', '') || E'\n';
            ELSIF part ->> 'type' = 'image' THEN
                extracted := extracted || coalesce(part -> 'image' ->> 'name', '') || E'\n';
            ELSIF part ->> 'type' = 'file' THEN
                extracted := extracted || coalesce(part -> 'file' ->> 'name', '') || E'\n';
            END IF;
        END LOOP;
    ELSIF role = 'user' THEN
        extracted := coalesce(content ->> 'user_text', CASE WHEN jsonb_typeof(content -> 'content') = 'string' THEN content ->> 'content' END, '');
    ELSIF role = 'assistant' THEN
        IF jsonb_typeof(content -> 'content') = 'string' THEN
            extracted := content ->> 'content';
        ELSIF jsonb_typeof(content -> 'content') = 'array' THEN
            FOR part IN SELECT value FROM jsonb_array_elements(content -> 'content') LOOP
                IF part ->> 'type' = 'text' THEN
                    extracted := extracted || coalesce(part ->> 'text', '');
                END IF;
            END LOOP;
        END IF;
    END IF;
    RETURN trim(extracted);
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION chat_message_text(content jsonb, role text) RETURNS text
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    extracted text := '';
    part jsonb;
BEGIN
    IF role = 'user' AND jsonb_typeof(content -> 'parts') = 'array' THEN
        FOR part IN SELECT value FROM jsonb_array_elements(content -> 'parts') LOOP
            IF part ->> 'type' = 'text' THEN
                extracted := extracted || coalesce(part ->> 'text', '') || E'\n';
            ELSIF part ->> 'type' = 'quote' THEN
                extracted := extracted || coalesce(part -> 'quote' ->> 'text', '') || E'\n';
            END IF;
        END LOOP;
    ELSIF role = 'user' THEN
        extracted := coalesce(content ->> 'user_text', CASE WHEN jsonb_typeof(content -> 'content') = 'string' THEN content ->> 'content' END, '');
    ELSIF role = 'assistant' THEN
        IF jsonb_typeof(content -> 'content') = 'string' THEN
            extracted := content ->> 'content';
        ELSIF jsonb_typeof(content -> 'content') = 'array' THEN
            FOR part IN SELECT value FROM jsonb_array_elements(content -> 'content') LOOP
                IF part ->> 'type' = 'text' THEN
                    extracted := extracted || coalesce(part ->> 'text', '');
                END IF;
            END LOOP;
        END IF;
    END IF;
    RETURN trim(extracted);
END $$;
-- +goose StatementEnd

ALTER TABLE sandbox_workspaces DROP COLUMN synced_through_sequence;
