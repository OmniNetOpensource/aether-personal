-- 本迁移新增部署级 ChatGPT OAuth 订阅凭据表与临时授权流表，并允许自定义模型供应商使用 openai_responses 协议。

-- +goose Up
CREATE TABLE chatgpt_oauth (
    id integer PRIMARY KEY CHECK (id = 1),
    host_id text NOT NULL CHECK (length(host_id) BETWEEN 1 AND 128),
    client_id text NOT NULL DEFAULT '' CHECK (length(client_id) <= 256),
    account_sub text NOT NULL DEFAULT '' CHECK (length(account_sub) <= 256),
    account_email text NOT NULL DEFAULT '' CHECK (length(account_email) <= 320),
    access_token_ciphertext bytea,
    refresh_token_ciphertext bytea,
    access_expires_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE chatgpt_oauth_flows (
    state_hash bytea PRIMARY KEY,
    code_verifier_ciphertext bytea NOT NULL,
    nonce text NOT NULL CHECK (length(nonce) BETWEEN 1 AND 256),
    redirect_uri text NOT NULL CHECK (length(redirect_uri) BETWEEN 1 AND 512),
    initial_client_id text NOT NULL CHECK (length(initial_client_id) BETWEEN 1 AND 256),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE model_providers DROP CONSTRAINT model_providers_protocol_check;
ALTER TABLE model_providers ADD CONSTRAINT model_providers_protocol_check
    CHECK (protocol IN ('openai', 'openai_responses', 'gemini', 'claude'));

-- +goose Down
DELETE FROM model_providers WHERE protocol = 'openai_responses';
ALTER TABLE model_providers DROP CONSTRAINT model_providers_protocol_check;
ALTER TABLE model_providers ADD CONSTRAINT model_providers_protocol_check
    CHECK (protocol IN ('openai', 'gemini', 'claude'));

UPDATE user_model_settings
SET main_model = 'openrouter/free', main_context_length = NULL
WHERE main_model LIKE 'chatgpt:%';

UPDATE user_model_settings
SET subagent_model = NULL, subagent_context_length = NULL
WHERE subagent_model LIKE 'chatgpt:%';

DROP TABLE chatgpt_oauth_flows;
DROP TABLE chatgpt_oauth;
