-- +goose Up
CREATE TABLE repositories (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK (source_type IN ('local_git', 'github')),
    name TEXT NOT NULL CHECK (char_length(name) <= 120 AND name ~ '[^[:space:]]'),
    source TEXT NOT NULL CHECK (char_length(source) <= 2048 AND source ~ '[^[:space:]]'),
    canonical_source TEXT NOT NULL CHECK (char_length(canonical_source) <= 2048 AND canonical_source ~ '[^[:space:]]'),
    default_branch TEXT NOT NULL CHECK (char_length(default_branch) <= 255),
    description TEXT NOT NULL CHECK (char_length(description) <= 2000),
    remote_url TEXT NOT NULL CHECK (char_length(remote_url) <= 2048),
    local_path TEXT,
    github_owner TEXT,
    github_repo TEXT,
    github_repo_id BIGINT,
    github_visibility TEXT,
    github_stars INTEGER,
    github_language TEXT,
    last_synced_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT repositories_source_consistent CHECK (
        (source_type = 'local_git' AND local_path IS NOT NULL AND local_path <> ''
            AND github_owner IS NULL AND github_repo IS NULL AND github_repo_id IS NULL
            AND github_visibility IS NULL AND github_stars IS NULL AND github_language IS NULL)
        OR
        (source_type = 'github' AND local_path IS NULL AND github_owner IS NOT NULL
            AND github_repo IS NOT NULL AND github_repo_id IS NOT NULL)
    ),
    CONSTRAINT repositories_project_canonical_unique UNIQUE (project_id, canonical_source)
);

CREATE INDEX repositories_project_created_idx ON repositories (project_id, created_at DESC, id DESC);
CREATE INDEX repositories_project_type_created_idx ON repositories (project_id, source_type, created_at DESC);

-- +goose Down
DROP TABLE repositories;
