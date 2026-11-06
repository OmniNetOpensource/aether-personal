-- 本文件为每个用户增加可编辑的 memory.md 根文档；根独立保存核心约定和主题索引，不替代主题的原话来源。
-- +goose Up
CREATE TABLE memory_roots (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL UNIQUE REFERENCES users (id) ON DELETE CASCADE,
    content text NOT NULL CHECK (btrim(content) <> ''),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE memory_topics ADD CONSTRAINT memory_topics_root_reserved CHECK (topic <> 'memory.md');

-- +goose Down
ALTER TABLE memory_topics DROP CONSTRAINT memory_topics_root_reserved;
DROP TABLE memory_roots;
