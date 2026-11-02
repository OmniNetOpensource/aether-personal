-- +goose Up
-- 多段消息的文本必须先提取再拼接；否则 PostgreSQL 会对拼接出的 text 使用 ->>。
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
                extracted := extracted || (part ->> 'text') || E'\n';
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

-- +goose Down
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
