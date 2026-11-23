-- +goose Up
CREATE TABLE jobs (
    id UUID PRIMARY KEY,
    type TEXT NOT NULL CHECK (char_length(type) BETWEEN 1 AND 120 AND type ~ '^[a-z0-9][a-z0-9._:-]*$'),
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    result JSONB,
    idempotency_key TEXT CHECK (idempotency_key IS NULL OR (char_length(idempotency_key) <= 200 AND idempotency_key ~ '[^[:space:]]')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 100),
    timeout_seconds INTEGER NOT NULL DEFAULT 300 CHECK (timeout_seconds BETWEEN 1 AND 86400),
    run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_by TEXT,
    lease_token UUID,
    lease_until TIMESTAMPTZ,
    cancel_requested_at TIMESTAMPTZ,
    last_error TEXT CHECK (last_error IS NULL OR char_length(last_error) <= 4000),
    last_error_code TEXT CHECK (last_error_code IS NULL OR char_length(last_error_code) <= 64),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((status = 'running' AND locked_by IS NOT NULL AND lease_token IS NOT NULL AND lease_until IS NOT NULL)
        OR (status <> 'running' AND locked_by IS NULL AND lease_token IS NULL AND lease_until IS NULL)),
    CHECK ((status IN ('succeeded', 'failed', 'cancelled') AND finished_at IS NOT NULL)
        OR (status IN ('pending', 'running') AND finished_at IS NULL))
);

CREATE UNIQUE INDEX jobs_idempotency_key_unique ON jobs (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX jobs_claim_pending_idx ON jobs (run_at, created_at, id) WHERE status = 'pending';
CREATE INDEX jobs_running_lease_idx ON jobs (lease_until) WHERE status = 'running';
CREATE INDEX jobs_status_created_idx ON jobs (status, created_at DESC, id DESC);
CREATE INDEX jobs_created_idx ON jobs (created_at DESC, id DESC);

-- +goose Down
DROP TABLE jobs;
