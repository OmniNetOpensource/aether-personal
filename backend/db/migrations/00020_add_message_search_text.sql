-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE messages ADD COLUMN search_text text NOT NULL DEFAULT '';

-- message_search_text 从一条消息的 content JSON 中提取可搜索的纯文本：
-- 正文字符串、多段内容里的文本块，以及工具调用参数（send_message 的汇报文本只存在参数里）。
-- 摘要、工具结果等一切消息都走同一提取口径，搜索范围不按消息类型过滤。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION message_search_text(content jsonb) RETURNS text
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    extracted text := '';
    part jsonb;
    call jsonb;
BEGIN
    IF jsonb_typeof(content -> 'content') = 'string' THEN
        extracted := content ->> 'content';
    ELSIF jsonb_typeof(content -> 'content') = 'array' THEN
        FOR part IN SELECT value FROM jsonb_array_elements(content -> 'content') LOOP
            IF part ->> 'type' IN ('text', 'input_text') AND part ->> 'text' IS NOT NULL THEN
                extracted := extracted || part ->> 'text' || E'\n';
            END IF;
        END LOOP;
    END IF;
    IF jsonb_typeof(content -> 'tool_calls') = 'array' THEN
        FOR call IN SELECT value FROM jsonb_array_elements(content -> 'tool_calls') LOOP
            IF call -> 'function' ->> 'arguments' IS NOT NULL THEN
                extracted := extracted || E'\n' || (call -> 'function' ->> 'arguments');
            END IF;
        END LOOP;
    END IF;
    RETURN trim(extracted);
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION messages_search_text_trigger() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.search_text := message_search_text(NEW.content);
    RETURN NEW;
END $$;
-- +goose StatementEnd

CREATE TRIGGER messages_search_text
    BEFORE INSERT OR UPDATE OF content ON messages
    FOR EACH ROW EXECUTE FUNCTION messages_search_text_trigger();

UPDATE messages SET search_text = message_search_text(content);

CREATE INDEX messages_search_text_trgm_idx ON messages USING gin (search_text gin_trgm_ops);

-- +goose Down
DROP INDEX messages_search_text_trgm_idx;
DROP TRIGGER messages_search_text ON messages;
DROP FUNCTION messages_search_text_trigger();
DROP FUNCTION message_search_text(jsonb);
ALTER TABLE messages DROP COLUMN search_text;
DROP EXTENSION IF EXISTS pg_trgm;
