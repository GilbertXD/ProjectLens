-- +goose Up
ALTER TABLE repositories
    ADD COLUMN role TEXT NOT NULL DEFAULT 'primary' CHECK (role IN ('primary', 'reference')),
    ADD CONSTRAINT repositories_id_project_unique UNIQUE (id, project_id);
CREATE INDEX repositories_project_role_created_idx ON repositories (project_id, role, created_at DESC);

ALTER TABLE documents
    ADD COLUMN repository_id UUID,
    ADD CONSTRAINT documents_repository_scope_fkey
        FOREIGN KEY (repository_id, project_id) REFERENCES repositories (id, project_id) ON DELETE CASCADE;
CREATE INDEX documents_project_repo_updated_idx ON documents (project_id, repository_id, updated_at DESC, id DESC);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bump_document_search_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'UPDATE' OR OLD.title IS DISTINCT FROM NEW.title
        OR OLD.content IS DISTINCT FROM NEW.content OR OLD.version IS DISTINCT FROM NEW.version
        OR OLD.repository_id IS DISTINCT FROM NEW.repository_id THEN
        UPDATE projects SET search_version = search_version + 1
        WHERE id = COALESCE(NEW.project_id, OLD.project_id);
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bump_repository_search_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR OLD.name IS DISTINCT FROM NEW.name
        OR OLD.source IS DISTINCT FROM NEW.source OR OLD.canonical_source IS DISTINCT FROM NEW.canonical_source
        OR OLD.remote_url IS DISTINCT FROM NEW.remote_url OR OLD.github_owner IS DISTINCT FROM NEW.github_owner
        OR OLD.github_repo IS DISTINCT FROM NEW.github_repo OR OLD.role IS DISTINCT FROM NEW.role THEN
        UPDATE projects SET search_version = search_version + 1 WHERE id = OLD.project_id;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bump_document_search_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'UPDATE' OR OLD.title IS DISTINCT FROM NEW.title
        OR OLD.content IS DISTINCT FROM NEW.content OR OLD.version IS DISTINCT FROM NEW.version THEN
        UPDATE projects SET search_version = search_version + 1
        WHERE id = COALESCE(NEW.project_id, OLD.project_id);
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bump_repository_search_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR OLD.name IS DISTINCT FROM NEW.name
        OR OLD.source IS DISTINCT FROM NEW.source OR OLD.canonical_source IS DISTINCT FROM NEW.canonical_source
        OR OLD.remote_url IS DISTINCT FROM NEW.remote_url OR OLD.github_owner IS DISTINCT FROM NEW.github_owner
        OR OLD.github_repo IS DISTINCT FROM NEW.github_repo THEN
        UPDATE projects SET search_version = search_version + 1 WHERE id = OLD.project_id;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

DROP INDEX documents_project_repo_updated_idx;
ALTER TABLE documents DROP CONSTRAINT documents_repository_scope_fkey, DROP COLUMN repository_id;
DROP INDEX repositories_project_role_created_idx;
ALTER TABLE repositories DROP CONSTRAINT repositories_id_project_unique, DROP COLUMN role;
