package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/db"
	"projectlens/internal/domain"
)

type PostgresJobStore struct{ pool *pgxpool.Pool }

func NewPostgresJobStore(pool *pgxpool.Pool) *PostgresJobStore {
	return &PostgresJobStore{pool: pool}
}

const jobColumns = "id, type, status, payload, result, idempotency_key, attempt, max_attempts, timeout_seconds, run_at, locked_by, lease_token, lease_until, cancel_requested_at, last_error, last_error_code, started_at, finished_at, created_at, updated_at"

func jobDestinations(job *domain.Job) []any {
	return []any{&job.ID, &job.Type, &job.Status, &job.Payload, &job.Result, &job.IdempotencyKey,
		&job.Attempt, &job.MaxAttempts, &job.TimeoutSeconds, &job.RunAt, &job.LockedBy, &job.LeaseToken,
		&job.LeaseUntil, &job.CancelRequestedAt, &job.LastError, &job.LastErrorCode, &job.StartedAt,
		&job.FinishedAt, &job.CreatedAt, &job.UpdatedAt}
}

func scanJob(row pgx.Row) (domain.Job, error) {
	var job domain.Job
	err := row.Scan(jobDestinations(&job)...)
	if errors.Is(err, pgx.ErrNoRows) {
		err = fmt.Errorf("%w: job not found", domain.ErrNotFound)
	}
	return job, err
}

func (s *PostgresJobStore) Create(ctx context.Context, job domain.Job) (domain.Job, error) {
	var runAt *time.Time
	if !job.RunAt.IsZero() {
		runAt = &job.RunAt
	}
	created, err := scanJob(db.ConnFromContext(ctx, s.pool).QueryRow(ctx, `
        INSERT INTO jobs (id, type, status, payload, idempotency_key, max_attempts, timeout_seconds, run_at)
        VALUES ($1, $2, 'pending', $3, $4, $5, $6, COALESCE($7, NOW()))
        ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
        RETURNING `+jobColumns, job.ID, job.Type, job.Payload, job.IdempotencyKey, job.MaxAttempts, job.TimeoutSeconds, runAt))
	if errors.Is(err, domain.ErrNotFound) {
		return created, domain.ErrDuplicateJobKey
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "22P02", "22P05", "22003":
			return created, fmt.Errorf("%w: payload contains a value PostgreSQL cannot store as JSONB", domain.ErrInvalidInput)
		}
	}
	return created, err
}

func (s *PostgresJobStore) GetByID(ctx context.Context, id uuid.UUID) (domain.Job, error) {
	return scanJob(db.ConnFromContext(ctx, s.pool).QueryRow(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = $1", id))
}

func (s *PostgresJobStore) GetByIdempotencyKey(ctx context.Context, key string) (domain.Job, error) {
	return scanJob(db.ConnFromContext(ctx, s.pool).QueryRow(ctx, "SELECT "+jobColumns+" FROM jobs WHERE idempotency_key = $1", key))
}

func (s *PostgresJobStore) List(ctx context.Context, filter domain.ListJobsFilter) (domain.JobListResult, error) {
	result := domain.JobListResult{Items: []domain.Job{}, Limit: filter.Limit, Offset: filter.Offset}
	rows, err := db.ConnFromContext(ctx, s.pool).Query(ctx, `
        WITH matching AS NOT MATERIALIZED (
            SELECT `+jobColumns+` FROM jobs WHERE ($1 = '' OR status = $1) AND ($2 = '' OR type = $2)
        ), page AS (
            SELECT * FROM matching ORDER BY created_at DESC, id DESC LIMIT $3 OFFSET $4
        )
        SELECT page.*, totals.total FROM (SELECT count(*) AS total FROM matching) totals
        LEFT JOIN page ON true ORDER BY page.created_at DESC, page.id DESC`,
		filter.Status, filter.Type, int64(filter.Limit), int64(filter.Offset))
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		if rows.RawValues()[0] == nil {
			dest := make([]any, len(rows.RawValues()))
			dest[len(dest)-1] = &result.Total
			if err := rows.Scan(dest...); err != nil {
				return result, err
			}
			continue
		}
		var job domain.Job
		if err := rows.Scan(append(jobDestinations(&job), &result.Total)...); err != nil {
			return result, err
		}
		result.Items = append(result.Items, job)
	}
	return result, rows.Err()
}

func jobInterval(d time.Duration) pgtype.Interval {
	micros := d.Microseconds()
	if d > 0 && micros == 0 {
		micros = 1
	}
	return pgtype.Interval{Microseconds: micros, Valid: true}
}

func (s *PostgresJobStore) ClaimNext(ctx context.Context, workerID string, token uuid.UUID, duration time.Duration) (domain.Job, error) {
	return scanJob(db.ConnFromContext(ctx, s.pool).QueryRow(ctx, `
        WITH candidate AS (
            SELECT id FROM jobs WHERE status = 'pending' AND run_at <= NOW()
            ORDER BY run_at, created_at, id FOR UPDATE SKIP LOCKED LIMIT 1
        )
        UPDATE jobs SET status = 'running', locked_by = $1, lease_token = $2, lease_until = NOW() + $3::interval,
            attempt = attempt + 1, started_at = NOW(), updated_at = NOW()
        WHERE id = (SELECT id FROM candidate) RETURNING `+jobColumns, workerID, token, jobInterval(duration)))
}

const jobFence = " WHERE id = $1 AND status = 'running' AND locked_by = $2 AND lease_token = $3 AND lease_until > NOW()"
const clearJobLease = "locked_by = NULL, lease_token = NULL, lease_until = NULL, updated_at = NOW()"

func (s *PostgresJobStore) RenewLease(ctx context.Context, id uuid.UUID, workerID string, token uuid.UUID, duration time.Duration) (bool, bool, error) {
	var requested bool
	err := db.ConnFromContext(ctx, s.pool).QueryRow(ctx,
		"UPDATE jobs SET lease_until = NOW() + $4::interval, updated_at = NOW()"+jobFence+
			" RETURNING cancel_requested_at IS NOT NULL", id, workerID, token, jobInterval(duration)).Scan(&requested)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return requested, err == nil, err
}

func (s *PostgresJobStore) CompleteSucceeded(ctx context.Context, id uuid.UUID, workerID string, token uuid.UUID, result json.RawMessage) (bool, error) {
	tag, err := db.ConnFromContext(ctx, s.pool).Exec(ctx, `
        UPDATE jobs SET status = 'succeeded', result = $4, finished_at = NOW(), last_error = NULL, last_error_code = NULL, `+
		clearJobLease+jobFence+" AND cancel_requested_at IS NULL", id, workerID, token, result)
	return tag.RowsAffected() != 0, err
}

func (s *PostgresJobStore) CompleteFailed(ctx context.Context, id uuid.UUID, workerID string, token uuid.UUID, code, message string, retryable bool, delay time.Duration) (bool, error) {
	tag, err := db.ConnFromContext(ctx, s.pool).Exec(ctx, `
        UPDATE jobs SET
            status = CASE WHEN cancel_requested_at IS NOT NULL THEN 'cancelled'
                WHEN $6 AND attempt < max_attempts THEN 'pending' ELSE 'failed' END,
            run_at = CASE WHEN cancel_requested_at IS NULL AND $6 AND attempt < max_attempts THEN NOW() + $7::interval ELSE run_at END,
            finished_at = CASE WHEN cancel_requested_at IS NULL AND $6 AND attempt < max_attempts THEN NULL ELSE NOW() END,
            last_error_code = CASE WHEN cancel_requested_at IS NOT NULL THEN 'cancelled' ELSE $4 END,
            last_error = CASE WHEN cancel_requested_at IS NOT NULL THEN 'cancelled by request' ELSE $5 END,
            `+clearJobLease+jobFence, id, workerID, token, jobErrorText(code, 64), jobErrorText(message, 4000), retryable, jobInterval(delay))
	return tag.RowsAffected() != 0, err
}

func jobErrorText(value string, length int) string {
	value = strings.ReplaceAll(strings.ToValidUTF8(value, "�"), "\x00", "�")
	for i := range value {
		if length == 0 {
			return value[:i]
		}
		length--
	}
	return value
}

func (s *PostgresJobStore) MarkCancelled(ctx context.Context, id uuid.UUID, workerID string, token uuid.UUID, reason string) (bool, error) {
	tag, err := db.ConnFromContext(ctx, s.pool).Exec(ctx, `
        UPDATE jobs SET status = 'cancelled', finished_at = NOW(), last_error_code = 'cancelled', last_error = $4,
        `+clearJobLease+jobFence+" AND cancel_requested_at IS NOT NULL", id, workerID, token, jobErrorText(reason, 4000))
	return tag.RowsAffected() != 0, err
}

func (s *PostgresJobStore) RequestCancel(ctx context.Context, id uuid.UUID) (domain.Job, error) {
	job, err := scanJob(db.ConnFromContext(ctx, s.pool).QueryRow(ctx, `
        UPDATE jobs SET
            status = CASE WHEN status = 'pending' THEN 'cancelled' ELSE status END,
            cancel_requested_at = COALESCE(cancel_requested_at, NOW()),
            finished_at = CASE WHEN status = 'pending' THEN NOW() ELSE finished_at END,
            last_error_code = CASE WHEN status = 'pending' THEN 'cancelled' ELSE last_error_code END,
            last_error = CASE WHEN status = 'pending' THEN 'cancelled by request' ELSE last_error END,
            updated_at = NOW()
        WHERE id = $1 AND status IN ('pending', 'running') RETURNING `+jobColumns, id))
	if !errors.Is(err, domain.ErrNotFound) {
		return job, err
	}
	job, err = s.GetByID(ctx, id)
	if err == nil && job.Status != domain.JobCancelled {
		err = fmt.Errorf("%w: job already %s", domain.ErrConflict, job.Status)
	}
	return job, err
}

func (s *PostgresJobStore) ReclaimExpiredLeases(ctx context.Context, batchSize int, delay time.Duration) (int64, error) {
	tag, err := db.ConnFromContext(ctx, s.pool).Exec(ctx, `
        WITH expired AS (
            SELECT id FROM jobs WHERE status = 'running' AND lease_until <= NOW()
            ORDER BY lease_until LIMIT $1 FOR UPDATE SKIP LOCKED
        )
        UPDATE jobs SET
            status = CASE WHEN cancel_requested_at IS NOT NULL THEN 'cancelled'
                WHEN attempt < max_attempts THEN 'pending' ELSE 'failed' END,
            run_at = CASE WHEN cancel_requested_at IS NULL AND attempt < max_attempts THEN NOW() + $2::interval ELSE run_at END,
            finished_at = CASE WHEN cancel_requested_at IS NULL AND attempt < max_attempts THEN NULL ELSE NOW() END,
            last_error_code = CASE WHEN cancel_requested_at IS NOT NULL THEN 'cancelled' ELSE 'lease_expired' END,
            last_error = CASE WHEN cancel_requested_at IS NOT NULL THEN 'cancelled by request'
                WHEN attempt < max_attempts THEN 'worker lease expired before completion'
                ELSE 'worker lease expired and max attempts reached' END,
            `+clearJobLease+" WHERE id IN (SELECT id FROM expired)", int64(batchSize), jobInterval(delay))
	return tag.RowsAffected(), err
}

func (s *PostgresJobStore) CreateSync(ctx context.Context, job domain.Job, target domain.SyncTarget) (out domain.Job, created bool, err error) {
	err = db.NewPgxTxManager(s.pool).WithinTx(ctx, func(ctx context.Context) error {
		conn := db.ConnFromContext(ctx, s.pool)
		var source string
		err := conn.QueryRow(ctx, "SELECT source_type FROM repositories WHERE id=$1 AND project_id=$2 FOR UPDATE", target.RepositoryID, target.ProjectID).Scan(&source)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: repository not found", domain.ErrNotFound)
		}
		if err != nil {
			return err
		}
		if job.Type == "repository.github_activity.sync" && (source != "github" || target.Ref != "HEAD") {
			return fmt.Errorf("%w: activity sync requires a GitHub repository and no ref", domain.ErrInvalidInput)
		}
		if job.IdempotencyKey != nil {
			existing, e := s.GetByIdempotencyKey(ctx, *job.IdempotencyKey)
			if e == nil {
				return matchSync(existing, job, target, &out)
			}
			if !errors.Is(e, domain.ErrNotFound) {
				return e
			}
		}
		existing, e := scanJob(conn.QueryRow(ctx, "SELECT "+jobColumns+" FROM jobs WHERE type=$1 AND lower(payload->>'repository_id')=$2 AND status IN ('pending','running') ORDER BY created_at LIMIT 1", job.Type, target.RepositoryID.String()))
		if e == nil {
			if job.IdempotencyKey != nil && (existing.IdempotencyKey == nil || *existing.IdempotencyKey != *job.IdempotencyKey) {
				return fmt.Errorf("%w: an active sync already owns this target; use its job or the scoped sync endpoint", domain.ErrConflict)
			}
			return matchSync(existing, job, target, &out)
		}
		if !errors.Is(e, domain.ErrNotFound) {
			return e
		}
		out, e = s.Create(ctx, job)
		if errors.Is(e, domain.ErrDuplicateJobKey) {
			return fmt.Errorf("%w: idempotency key already used", domain.ErrConflict)
		}
		created = e == nil
		return e
	})
	return
}
func matchSync(existing, requested domain.Job, target domain.SyncTarget, out *domain.Job) error {
	var active domain.SyncTarget
	if json.Unmarshal(existing.Payload, &active) != nil {
		return domain.ErrConflict
	}
	if active.Ref == "" {
		active.Ref = "HEAD"
	}
	if existing.Type != requested.Type || active != target {
		return fmt.Errorf("%w: active job or idempotency key has a different target", domain.ErrConflict)
	}
	*out = existing
	return nil
}
