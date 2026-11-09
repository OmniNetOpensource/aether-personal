-- +goose Up
-- 页面搜索只提取聊天能展示的文字，不把同一行附带的工具参数或模型输入包装放进结果。
-- +goose StatementBegin
CREATE FUNCTION chat_message_text(content jsonb, role text) RETURNS text
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

-- 表达式索引直接基于原消息建立，不另存一份正文；ILIKE 保留中英文的字面匹配。
CREATE INDEX messages_chat_text_trgm_idx ON messages
    USING gin (chat_message_text(content, role) gin_trgm_ops) WHERE visible;

-- +goose Down
DROP INDEX messages_chat_text_trgm_idx;
DROP FUNCTION chat_message_text(jsonb, text);
