-- +goose Up
ALTER TABLE jobs ADD COLUMN trace_parent varchar(55) NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE jobs DROP COLUMN trace_parent;
