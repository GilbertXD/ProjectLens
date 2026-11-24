-- +goose Up
CREATE INDEX jobs_active_sync_target ON jobs(type,lower(payload->>'repository_id'),created_at)
 WHERE status IN ('pending','running');
-- +goose Down
DROP INDEX jobs_active_sync_target;
