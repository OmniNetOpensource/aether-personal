-- 本文件新增 MCP OAuth 2.1 动态注册客户端、一次性授权码与访问/刷新令牌表，所有敏感凭据仅存哈希。
-- +goose Up
CREATE TABLE oauth_clients (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    client_name text NOT NULL CHECK (btrim(client_name) <> ''),
    redirect_uris text[] NOT NULL CHECK (cardinality(redirect_uris) > 0),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE oauth_authorization_codes (
    code_hash bytea PRIMARY KEY,
    client_id uuid NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    redirect_uri text NOT NULL CHECK (redirect_uri <> ''),
    code_challenge text NOT NULL CHECK (code_challenge <> ''),
    scope text NOT NULL CHECK (scope = 'memory'),
    resource text NOT NULL CHECK (resource <> ''),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX oauth_authorization_codes_user_id_idx ON oauth_authorization_codes (user_id);
CREATE INDEX oauth_authorization_codes_client_id_idx ON oauth_authorization_codes (client_id);

CREATE TABLE oauth_tokens (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    client_id uuid NOT NULL REFERENCES oauth_clients (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    scope text NOT NULL CHECK (scope = 'memory'),
    resource text NOT NULL CHECK (resource <> ''),
    access_token_hash bytea NOT NULL UNIQUE,
    access_expires_at timestamptz NOT NULL,
    refresh_token_hash bytea NOT NULL UNIQUE,
    refresh_expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);

CREATE INDEX oauth_tokens_user_id_idx ON oauth_tokens (user_id);
CREATE INDEX oauth_tokens_client_id_idx ON oauth_tokens (client_id);

-- +goose Down
DROP TABLE oauth_tokens;
DROP TABLE oauth_authorization_codes;
DROP TABLE oauth_clients;
