-- +goose Up
ALTER TABLE service_settings DROP CONSTRAINT service_settings_service_check;
ALTER TABLE service_settings ADD CONSTRAINT service_settings_service_check CHECK (service IN ('openrouter', 'ikun_gemini', 'resend', 'serper', 'parallel', 'everyinfra', 'aliyun_oss'));

-- +goose Down
DELETE FROM service_settings WHERE service = 'aliyun_oss';
ALTER TABLE service_settings DROP CONSTRAINT service_settings_service_check;
ALTER TABLE service_settings ADD CONSTRAINT service_settings_service_check CHECK (service IN ('openrouter', 'ikun_gemini', 'resend', 'serper', 'parallel', 'everyinfra'));
