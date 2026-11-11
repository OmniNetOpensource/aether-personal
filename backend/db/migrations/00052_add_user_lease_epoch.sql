-- 本迁移为用户记录单调递增的 Agent 执行代数，防止租约过期或 Redis 重启后旧实例覆盖新实例的数据。

-- +goose Up
ALTER TABLE users ADD COLUMN main_lease_epoch bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE users DROP COLUMN main_lease_epoch;
