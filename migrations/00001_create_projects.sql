-- +goose Up
CREATE TABLE projects (
    id UUID PRIMARY KEY,
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT projects_slug_unique UNIQUE (slug),
    CONSTRAINT projects_slug_format_check CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$' AND char_length(slug) BETWEEN 2 AND 64),
    CONSTRAINT projects_name_not_blank_check CHECK (btrim(name) <> '' AND char_length(name) <= 120),
    CONSTRAINT projects_description_length_check CHECK (char_length(description) <= 2000),
    CONSTRAINT projects_status_check CHECK (status IN ('active', 'archived'))
);
CREATE INDEX idx_projects_status_created_at ON projects (status, created_at DESC);

CREATE TABLE project_audit_logs (
    id BIGSERIAL PRIMARY KEY,
    project_id UUID REFERENCES projects(id) ON DELETE SET NULL,
    project_slug TEXT NOT NULL,
    action TEXT NOT NULL CONSTRAINT project_audit_logs_action_check CHECK (action IN ('created', 'updated', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_project_audit_logs_project_id_created_at ON project_audit_logs (project_id, created_at DESC);
CREATE INDEX idx_project_audit_logs_project_slug_created_at ON project_audit_logs (project_slug, created_at DESC);

-- +goose Down
DROP TABLE project_audit_logs;
DROP TABLE projects;
