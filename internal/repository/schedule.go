package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"projectlens/internal/db"
	"projectlens/internal/domain"
)

func (r *PostgresRepositoryStore) SetPolicy(ctx context.Context, id uuid.UUID, kind string, in domain.PolicyInput) error {
	_, err := db.ConnFromContext(ctx, r.pool).Exec(ctx, `INSERT INTO sync_policies(repository_id,kind,enabled,interval_seconds,next_at) VALUES($1,$2,$3,$4,now())
 ON CONFLICT(repository_id,kind) DO UPDATE SET enabled=excluded.enabled,interval_seconds=excluded.interval_seconds,
 next_at=CASE WHEN NOT sync_policies.enabled AND excluded.enabled THEN now()
 WHEN sync_policies.interval_seconds<>excluded.interval_seconds THEN now()+make_interval(secs=>excluded.interval_seconds)
 ELSE sync_policies.next_at END`, id, kind, *in.Enabled, in.IntervalSeconds)
	return err
}
func (r *PostgresRepositoryStore) Policies(ctx context.Context, id uuid.UUID) ([]domain.Policy, error) {
	rows, err := db.ConnFromContext(ctx, r.pool).Query(ctx, `SELECT p.kind,p.enabled,p.interval_seconds,CASE WHEN enabled THEN next_at END,coalesce((SELECT id::text FROM jobs WHERE payload->>'repository_id'=p.repository_id::text AND type=t.jobtype ORDER BY created_at DESC LIMIT 1),p.last_job::text),
 (SELECT max(finished_at) FROM jobs WHERE payload->>'repository_id'=p.repository_id::text AND type=t.jobtype AND status='succeeded'),
 (SELECT last_error_code FROM jobs WHERE payload->>'repository_id'=p.repository_id::text AND type=t.jobtype AND last_error_code IS NOT NULL ORDER BY updated_at DESC LIMIT 1)
 FROM sync_policies p JOIN (VALUES ('files','repository.sync'),('commits','repository.commits.sync'),('github_activity','repository.github_activity.sync')) t(kind,jobtype) USING(kind) WHERE repository_id=$1 ORDER BY p.kind`, id)
	if err != nil {
		return nil, err
	}
	return pgx.AppendRows([]domain.Policy{}, rows, pgx.RowToStructByPos[domain.Policy])
}

type DuePolicy struct {
	Project, Repo uuid.UUID
	Kind          string
}

func (r *PostgresRepositoryStore) DuePolicies(ctx context.Context, batch int) ([]DuePolicy, error) {
	conn := db.ConnFromContext(ctx, r.pool)
	var tick bool
	err := conn.QueryRow(ctx, "UPDATE sync_scheduler_clock SET next_tick=now()+interval '5 seconds' WHERE id AND next_tick<=now() RETURNING id").Scan(&tick)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, `SELECT r.project_id,p.repository_id,p.kind FROM repositories r JOIN sync_policies p ON r.id=p.repository_id WHERE enabled AND next_at<=now() ORDER BY next_at,p.repository_id,p.kind LIMIT $1 FOR UPDATE OF r, p SKIP LOCKED`, batch)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DuePolicy])
}
func (r *PostgresRepositoryStore) NextPolicy(ctx context.Context, p DuePolicy, job *uuid.UUID, after *time.Time, minimum int) error {
	_, err := db.ConnFromContext(ctx, r.pool).Exec(ctx, `UPDATE sync_policies SET next_at=greatest(now()+make_interval(secs=>greatest(interval_seconds,$5)),$4::timestamptz),last_job=coalesce($3,last_job) WHERE repository_id=$1 AND kind=$2`, p.Repo, p.Kind, job, after, minimum)
	return err
}
func (r *PostgresRepositoryStore) ActivityBackoff(ctx context.Context, id uuid.UUID) (at *time.Time, err error) {
	err = db.ConnFromContext(ctx, r.pool).QueryRow(ctx, `SELECT max(run_at) FROM jobs WHERE type='repository.github_activity.sync' AND payload->>'repository_id'=$1 AND last_error_code='github_rate_limit' AND run_at>now()`, id.String()).Scan(&at)
	return
}
