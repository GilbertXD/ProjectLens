-- +goose Up
CREATE TABLE github_activity (
 repository_id uuid REFERENCES repositories ON DELETE CASCADE,
 kind text NOT NULL, native_id text NOT NULL, number int NOT NULL,
 data jsonb NOT NULL, seen uuid NOT NULL, etag text NOT NULL, PRIMARY KEY(repository_id,kind,number,native_id)
);
CREATE INDEX github_activity_list ON github_activity(repository_id,kind,number DESC,native_id);
CREATE TABLE github_activity_state (
 repository_id uuid PRIMARY KEY REFERENCES repositories ON DELETE CASCADE,
 cursor_at timestamptz NOT NULL, reconciled_at timestamptz NOT NULL
);
-- +goose Down
DROP TABLE github_activity_state,github_activity;
