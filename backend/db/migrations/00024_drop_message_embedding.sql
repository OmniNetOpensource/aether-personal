-- +goose Up
-- 删除向量检索：messages.embedding 列、向量索引、待补算索引与 pgvector 扩展一并移除，历史检索只保留关键词搜索。
DROP INDEX messages_embedding_idx;
DROP INDEX messages_embedding_pending_idx;
ALTER TABLE messages DROP COLUMN embedding;
DROP EXTENSION vector;

-- +goose Down
CREATE EXTENSION IF NOT EXISTS vector;
ALTER TABLE messages ADD COLUMN embedding vector(1536);
CREATE INDEX messages_embedding_pending_idx ON messages (id) WHERE embedding IS NULL;
CREATE INDEX messages_embedding_idx ON messages USING hnsw (embedding vector_cosine_ops);
