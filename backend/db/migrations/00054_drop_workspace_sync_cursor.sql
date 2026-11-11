-- 本迁移删除长期工作区记录中的附件自动同步游标，聊天图片与文件改为由 sandbox_import_file 按需导入 /mnt/workspace。

-- +goose Up
ALTER TABLE sandbox_workspaces DROP COLUMN synced_through_sequence;

-- +goose Down
ALTER TABLE sandbox_workspaces ADD COLUMN synced_through_sequence bigint NOT NULL DEFAULT 0;
