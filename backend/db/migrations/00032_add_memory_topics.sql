-- 本文件新增由 Main 维护的主题记忆：正文独立于 compact 保存，来源、版本与删除标记用于更新和追溯。
-- +goose Up
CREATE TABLE memory_topics (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    topic text NOT NULL CHECK (topic <> ''),
    title text NOT NULL,
    summary text NOT NULL,
    content text NOT NULL,
    source_message_id uuid NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    forgotten_through_sequence bigint,
    UNIQUE (user_id, topic),
    CHECK (
        (deleted_at IS NULL AND title <> '' AND summary <> '' AND content <> '')
        OR (deleted_at IS NOT NULL AND title = '' AND summary = '' AND content = '')
    ),
    CHECK (deleted_at IS NULL OR forgotten_through_sequence IS NOT NULL)
);

-- +goose Down
DROP TABLE memory_topics;
