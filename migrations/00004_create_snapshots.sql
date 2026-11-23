-- +goose Up
CREATE TABLE snapshots (
    id UUID PRIMARY KEY,
    repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    job_id UUID REFERENCES jobs(id) ON DELETE SET NULL,
    parent_snapshot_id UUID REFERENCES snapshots(id) ON DELETE SET NULL,
    version BIGINT NOT NULL CHECK (version >= 1),
    status TEXT NOT NULL CHECK (status IN ('staging', 'active', 'superseded', 'failed')),
    resolved_ref TEXT NOT NULL CHECK (char_length(resolved_ref) BETWEEN 1 AND 255),
    commit_hash TEXT NOT NULL CHECK (commit_hash ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    tree_hash TEXT NOT NULL CHECK (tree_hash ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    scanner_config_hash TEXT NOT NULL CHECK (char_length(scanner_config_hash) = 64),
    commit_author_name TEXT NOT NULL,
    commit_author_email TEXT NOT NULL,
    commit_authored_at TIMESTAMPTZ NOT NULL,
    commit_committer_name TEXT NOT NULL,
    commit_committer_email TEXT NOT NULL,
    commit_committed_at TIMESTAMPTZ NOT NULL,
    commit_subject TEXT NOT NULL,
    commit_parents JSONB NOT NULL DEFAULT '[]'::jsonb,
    total_tree_entries BIGINT NOT NULL CHECK (total_tree_entries >= 0),
    included_files BIGINT NOT NULL CHECK (included_files >= 0),
    included_bytes BIGINT NOT NULL CHECK (included_bytes >= 0),
    skipped_files BIGINT NOT NULL CHECK (skipped_files >= 0),
    skipped_by_reason JSONB NOT NULL DEFAULT '{}'::jsonb,
    truncated BOOLEAN NOT NULL DEFAULT FALSE,
    added_files BIGINT NOT NULL CHECK (added_files >= 0),
    modified_files BIGINT NOT NULL CHECK (modified_files >= 0),
    deleted_files BIGINT NOT NULL CHECK (deleted_files >= 0),
    unchanged_files BIGINT NOT NULL CHECK (unchanged_files >= 0),
    blobs_read BIGINT NOT NULL CHECK (blobs_read >= 0),
    error_message TEXT CHECK (error_message IS NULL OR char_length(error_message) <= 4000),
    scanned_at TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ,
    superseded_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (repository_id, version),
    CONSTRAINT snapshots_status_timestamps CHECK (
        (status = 'active' AND published_at IS NOT NULL AND superseded_at IS NULL)
        OR (status = 'superseded' AND published_at IS NOT NULL AND superseded_at IS NOT NULL)
        OR (status IN ('staging', 'failed') AND published_at IS NULL)
    )
);

CREATE UNIQUE INDEX snapshots_one_active_per_repo_idx ON snapshots (repository_id) WHERE status = 'active';
CREATE INDEX snapshots_repo_version_idx ON snapshots (repository_id, version DESC);
CREATE INDEX snapshots_gc_idx ON snapshots (repository_id, status, created_at ASC, version ASC)
    WHERE status IN ('superseded', 'failed', 'staging');

CREATE TABLE source_files (
    id UUID PRIMARY KEY,
    snapshot_id UUID NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    path TEXT NOT NULL CHECK (char_length(path) BETWEEN 1 AND 512),
    blob_oid TEXT NOT NULL CHECK (blob_oid ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    mode TEXT NOT NULL CHECK (mode IN ('100644', '100755')),
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    line_count INTEGER NOT NULL CHECK (line_count >= 0),
    is_executable BOOLEAN NOT NULL DEFAULT FALSE,
    content TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (snapshot_id, path)
);

CREATE INDEX source_files_snapshot_blob_idx ON source_files (snapshot_id, blob_oid);
CREATE INDEX source_files_repo_snapshot_path_idx ON source_files (repository_id, snapshot_id, path);

-- +goose Down
DROP TABLE source_files;
DROP TABLE snapshots;
