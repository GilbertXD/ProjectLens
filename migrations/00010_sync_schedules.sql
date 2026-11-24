-- +goose Up
CREATE TABLE sync_policies (
 repository_id uuid REFERENCES repositories ON DELETE CASCADE,
 kind text NOT NULL CHECK (kind IN ('files','commits','github_activity')),
 enabled boolean NOT NULL, interval_seconds int NOT NULL CHECK(interval_seconds>0),
 next_at timestamptz NOT NULL, last_job uuid REFERENCES jobs ON DELETE SET NULL,
 PRIMARY KEY(repository_id,kind)
);
CREATE INDEX sync_policies_due ON sync_policies(next_at,repository_id,kind) WHERE enabled;
CREATE TABLE sync_scheduler_clock(id boolean PRIMARY KEY CHECK(id), next_tick timestamptz NOT NULL);
INSERT INTO sync_scheduler_clock VALUES(true,now());
CREATE INDEX jobs_sync_outcomes ON jobs ((payload->>'repository_id'),type,finished_at DESC) WHERE status IN ('failed','succeeded','pending');
-- +goose Down
DROP INDEX jobs_sync_outcomes;
DROP TABLE sync_scheduler_clock,sync_policies;
