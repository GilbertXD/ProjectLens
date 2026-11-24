-- +goose Up
-- Repository-owned documents are user-authored notes, so disassociating a
-- repository must not destroy them. The composite foreign key keeps enforcing
-- that a document may only reference a repository of its own project, but now
-- clears the document's repository scope instead of deleting the row.
-- Survivors record the disassociated repository as informational provenance
-- under metadata.origin. Column-level SET NULL needs PostgreSQL 15 or newer.
ALTER TABLE documents ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(metadata) = 'object');

ALTER TABLE documents DROP CONSTRAINT documents_repository_scope_fkey;
ALTER TABLE documents ADD CONSTRAINT documents_repository_scope_fkey
    FOREIGN KEY (repository_id, project_id) REFERENCES repositories (id, project_id)
    ON DELETE SET NULL (repository_id);

-- +goose Down
ALTER TABLE documents DROP CONSTRAINT documents_repository_scope_fkey;
ALTER TABLE documents ADD CONSTRAINT documents_repository_scope_fkey
    FOREIGN KEY (repository_id, project_id) REFERENCES repositories (id, project_id) ON DELETE CASCADE;
ALTER TABLE documents DROP COLUMN metadata;
