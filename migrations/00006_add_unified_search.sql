-- +goose Up
ALTER TABLE projects ADD COLUMN search_version BIGINT NOT NULL DEFAULT 1 CHECK (search_version >= 1);

ALTER TABLE source_files ADD COLUMN search_vector tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', coalesce(path, '') || ' ' || regexp_replace(coalesce(path, ''), '[/._-]+', ' ', 'g')), 'A')
    || setweight(to_tsvector('simple', coalesce(content, '')), 'B')
) STORED;
CREATE INDEX idx_source_files_search_vector ON source_files USING GIN (search_vector);

ALTER TABLE documents ADD COLUMN search_vector tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', coalesce(title, '')), 'A')
    || setweight(to_tsvector('simple', coalesce(content, '')), 'B')
) STORED;
CREATE INDEX idx_documents_search_vector ON documents USING GIN (search_vector);

-- +goose StatementBegin
CREATE FUNCTION bump_document_search_version() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER documents_search_version
AFTER INSERT OR UPDATE OR DELETE ON documents
FOR EACH ROW EXECUTE FUNCTION bump_document_search_version();

-- +goose StatementBegin
CREATE FUNCTION bump_snapshot_search_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (TG_OP IN ('INSERT', 'UPDATE') AND NEW.status = 'active')
        OR (TG_OP IN ('UPDATE', 'DELETE') AND OLD.status = 'active') THEN
        UPDATE projects SET search_version = search_version + 1
        WHERE id = (SELECT project_id FROM repositories WHERE id = COALESCE(NEW.repository_id, OLD.repository_id));
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER snapshots_search_version
AFTER INSERT OR UPDATE OR DELETE ON snapshots
FOR EACH ROW EXECUTE FUNCTION bump_snapshot_search_version();

-- +goose StatementBegin
CREATE FUNCTION bump_repository_search_version() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER repositories_search_version
AFTER UPDATE OR DELETE ON repositories
FOR EACH ROW EXECUTE FUNCTION bump_repository_search_version();

-- +goose Down
DROP TRIGGER repositories_search_version ON repositories;
DROP TRIGGER snapshots_search_version ON snapshots;
DROP TRIGGER documents_search_version ON documents;
DROP FUNCTION bump_repository_search_version();
DROP FUNCTION bump_snapshot_search_version();
DROP FUNCTION bump_document_search_version();
DROP INDEX idx_documents_search_vector;
DROP INDEX idx_source_files_search_vector;
ALTER TABLE documents DROP COLUMN search_vector;
ALTER TABLE source_files DROP COLUMN search_vector;
ALTER TABLE projects DROP COLUMN search_version;
