-- +goose Up
-- 管理员在看板填写的请求输出上限；为空表示还没在看板保存过，运行时回退到部署默认值（MAX_OUTPUT_TOKENS，未设置时 8192）。
ALTER TABLE model_settings
    ADD COLUMN main_output_limit integer CHECK (main_output_limit BETWEEN 1 AND 2000000);

-- +goose Down
ALTER TABLE model_settings DROP COLUMN main_output_limit;
