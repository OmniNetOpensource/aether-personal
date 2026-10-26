-- +goose Up
CREATE TABLE model_settings (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    singleton boolean NOT NULL DEFAULT true UNIQUE CHECK (singleton),
    main_model text NOT NULL CHECK (length(main_model) BETWEEN 1 AND 160),
    subagent_model text CHECK (length(subagent_model) BETWEEN 1 AND 160),
    version bigint NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text NOT NULL
);

-- +goose Down
DROP TABLE model_settings;
