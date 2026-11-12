-- 本迁移新增消息多选分享记录表与消息关联表，支持生成不可猜测的公开分享链接与随时取消分享。

-- +goose Up
CREATE TABLE message_shares (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token text NOT NULL UNIQUE CHECK (token ~ '^[A-Za-z0-9_-]{22,64}$'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX message_shares_user_idx ON message_shares (user_id, id DESC);

CREATE TABLE message_share_items (
    share_id uuid NOT NULL REFERENCES message_shares(id) ON DELETE CASCADE,
    message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    PRIMARY KEY (share_id, message_id)
);
CREATE INDEX message_share_items_message_idx ON message_share_items (message_id);

-- +goose Down
DROP TABLE message_share_items;
DROP TABLE message_shares;
