-- +goose Up
CREATE TABLE documents (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title TEXT NOT NULL CHECK (btrim(title) <> '' AND char_length(title) <= 200),
    content TEXT NOT NULL DEFAULT '' CHECK (octet_length(content) <= 1048576),
    content_bytes BIGINT GENERATED ALWAYS AS (octet_length(content)) STORED,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX documents_project_updated_idx ON documents (project_id, updated_at DESC, id DESC);
CREATE INDEX documents_project_created_idx ON documents (project_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE documents;
