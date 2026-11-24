-- +goose Up
CREATE TABLE repository_commits (
 repository_id uuid REFERENCES repositories ON DELETE CASCADE,
 sha text NOT NULL, metadata jsonb NOT NULL, changes jsonb NOT NULL,
 PRIMARY KEY(repository_id,sha)
);
CREATE TABLE repository_history_refs (
 repository_id uuid REFERENCES repositories ON DELETE CASCADE, ref text NOT NULL,
 tip text NOT NULL, reachable text[] NOT NULL, has_more boolean NOT NULL,
 synced_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(repository_id,ref)
);
-- +goose Down
DROP TABLE repository_history_refs,repository_commits;
