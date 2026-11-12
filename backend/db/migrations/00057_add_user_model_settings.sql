-- +goose Up
-- 单独保存各账号的模型设置，已有账号通过应用的默认免费路由开始使用。
CREATE TABLE user_model_settings (
    user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    main_model text NOT NULL CHECK (length(main_model) BETWEEN 1 AND 160),
    main_context_length bigint CHECK (main_context_length > 0),
    subagent_model text CHECK (length(subagent_model) BETWEEN 1 AND 160),
    subagent_context_length bigint CHECK (subagent_context_length > 0),
    compact_model text CHECK (length(compact_model) BETWEEN 1 AND 160),
    version bigint NOT NULL CHECK (version > 0)
);

-- +goose Down
DROP TABLE user_model_settings;
