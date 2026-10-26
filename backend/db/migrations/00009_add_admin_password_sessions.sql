-- +goose Up
CREATE TABLE admin_sessions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    token_hash bytea NOT NULL UNIQUE,
    credential_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL DEFAULT now() + interval '12 hours'
);

CREATE TABLE admin_login_attempts (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    attempts integer NOT NULL,
    window_started_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE admin_login_attempts;
DROP TABLE admin_sessions;
