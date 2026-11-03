-- 本文件移除手动输出额度；模型请求使用供应商目录的最大值，目录未知时不发送额度参数。
-- +goose Up
ALTER TABLE model_settings DROP COLUMN main_output_limit;

-- +goose Down
ALTER TABLE model_settings
    ADD COLUMN main_output_limit integer CHECK (main_output_limit BETWEEN 1 AND 2000000);
