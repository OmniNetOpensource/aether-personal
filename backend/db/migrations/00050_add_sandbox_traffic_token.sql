-- 本迁移加密保存实例流量令牌，使只读桌面监控无需连接、唤醒或续期计算实例。

-- +goose Up
ALTER TABLE sandbox_workspaces ADD COLUMN traffic_token_ciphertext bytea;
ALTER TABLE sandbox_workspaces ADD CONSTRAINT sandbox_traffic_requires_instance
    CHECK (traffic_token_ciphertext IS NULL OR sandbox_id IS NOT NULL);

-- +goose Down
ALTER TABLE sandbox_workspaces DROP CONSTRAINT sandbox_traffic_requires_instance;
ALTER TABLE sandbox_workspaces DROP COLUMN traffic_token_ciphertext;
