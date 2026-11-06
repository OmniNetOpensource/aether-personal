-- 本文件把根与主题统一为 PostgreSQL 中的虚拟文本文件，保留有效正文，移除来源、版本和根主题联动存储。
-- +goose Up
CREATE TABLE memory_files (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    path text NOT NULL CHECK (starts_with(path, '/memories/') AND path <> '/memories/'),
    content text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, path)
);

-- 已编辑的根原样保存；还没有根的既有用户，用有效主题生成一次索引骨架。
INSERT INTO memory_files (user_id, path, content, updated_at)
SELECT user_id, '/memories/memory.md', content, updated_at FROM memory_roots;

-- 旧主题键原本允许任意文本；非常规路径和文件/目录冲突保存在唯一的迁移路径，避免升级冲突或归一化后无法读回。
CREATE TEMP TABLE memory_topic_migration ON COMMIT DROP AS
WITH candidates AS (
    SELECT t.*, '/memories/' || topic AS proposed_path FROM memory_topics AS t WHERE deleted_at IS NULL
), mapped AS (
    SELECT c.*, topic ~ '^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$'
        AND topic !~ '(^|/)[.]{1,2}(/|$)'
        AND NOT starts_with(topic, 'memory.md/')
        AND topic <> '.migrated' AND NOT starts_with(topic, '.migrated/')
        AND NOT EXISTS (
            SELECT 1 FROM candidates AS other WHERE other.user_id = c.user_id AND other.id <> c.id
            AND (other.proposed_path = c.proposed_path
                OR starts_with(other.proposed_path, c.proposed_path || '/')
                OR starts_with(c.proposed_path, other.proposed_path || '/'))
        ) AS unchanged
    FROM candidates AS c
)
SELECT id, user_id, topic, title, content, updated_at, unchanged,
    CASE WHEN unchanged THEN proposed_path ELSE '/memories/.migrated/' || id::text || '.md' END AS path
FROM mapped;

INSERT INTO memory_files (user_id, path, content, updated_at)
SELECT user_id, path, content, updated_at FROM memory_topic_migration;

INSERT INTO memory_files (user_id, path, content)
SELECT user_id, '/memories/memory.md',
    E'# Memory\n\n## 核心约定\n\n## 主题索引\n\n' ||
    string_agg(format('- [%s](<%s>)', title, path), E'\n' ORDER BY path) || E'\n'
FROM memory_topic_migration GROUP BY user_id
ON CONFLICT (user_id, path) DO NOTHING;

-- 已有根只在路径被调整时追加修复索引，原来的核心约定和正文不覆盖。
UPDATE memory_files AS root SET content = root.content || E'\n\n## 迁移主题索引\n\n' || links.content || E'\n'
FROM (
    SELECT user_id, string_agg(format('- [迁移主题 %s](%s)：%s', id, path, replace(topic, E'\n', ' ')), E'\n' ORDER BY path) AS content
    FROM memory_topic_migration WHERE NOT unchanged GROUP BY user_id
) AS links
WHERE root.user_id = links.user_id AND root.path = '/memories/memory.md'
    AND EXISTS (SELECT 1 FROM memory_roots AS previous WHERE previous.user_id = root.user_id);

DROP TABLE memory_topic_migration;
DROP TABLE memory_roots;
DROP TABLE memory_topics;

-- +goose Down
-- 降级只恢复旧结构，无法还原已移除的来源、版本和删除记录；执行降级前须备份记忆数据。
DROP TABLE memory_files;

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
    CHECK ((deleted_at IS NULL AND title <> '' AND summary <> '' AND content <> '')
        OR (deleted_at IS NOT NULL AND title = '' AND summary = '' AND content = '')),
    CHECK (deleted_at IS NULL OR forgotten_through_sequence IS NOT NULL),
    CONSTRAINT memory_topics_root_reserved CHECK (topic <> 'memory.md')
);

CREATE TABLE memory_roots (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL UNIQUE REFERENCES users (id) ON DELETE CASCADE,
    content text NOT NULL CHECK (btrim(content) <> ''),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);
