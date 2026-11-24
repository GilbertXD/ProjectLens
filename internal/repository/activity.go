package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/github"
)

func (r *PostgresRepositoryStore) ActivityCursor(ctx context.Context, id uuid.UUID) (cursor, full time.Time, err error) {
	err = db.ConnFromContext(ctx, r.pool).QueryRow(ctx, "SELECT cursor_at,reconciled_at FROM github_activity_state WHERE repository_id=$1", id).Scan(&cursor, &full)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return
}
func (r *PostgresRepositoryStore) SaveActivity(ctx context.Context, id, batch uuid.UUID, kind, key string, n int, a github.Activity, etag string) error {
	_, err := db.ConnFromContext(ctx, r.pool).Exec(ctx, `INSERT INTO github_activity VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(repository_id,kind,number,native_id) DO UPDATE SET data=excluded.data,seen=excluded.seen,etag=excluded.etag`, id, kind, key, n, a, batch, etag)
	return err
}

type CachedPull struct {
	Activity github.Activity
	ETag     string
}

func (r *PostgresRepositoryStore) CachedPulls(ctx context.Context, id uuid.UUID) (map[int]CachedPull, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT ON (number) number,data,etag FROM github_activity
		WHERE repository_id=$1 AND kind='pull' ORDER BY number,native_id DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pulls := make(map[int]CachedPull)
	for rows.Next() {
		var n int
		var pull CachedPull
		if err := rows.Scan(&n, &pull.Activity, &pull.ETag); err != nil {
			return nil, err
		}
		pulls[n] = pull
	}
	return pulls, rows.Err()
}
func (r *PostgresRepositoryStore) ClearOldLinks(ctx context.Context, id, batch uuid.UUID, n int) error {
	_, err := db.ConnFromContext(ctx, r.pool).Exec(ctx, "DELETE FROM github_activity WHERE repository_id=$1 AND number=$2 AND kind='commit' AND seen<>$3", id, n, batch)
	return err
}
func (r *PostgresRepositoryStore) FinishActivity(ctx context.Context, id, batch uuid.UUID, cursor, full time.Time, reconcile bool) error {
	conn := db.ConnFromContext(ctx, r.pool)
	if reconcile {
		if _, err := conn.Exec(ctx, "DELETE FROM github_activity WHERE repository_id=$1 AND seen<>$2", id, batch); err != nil {
			return err
		}
	}
	_, err := conn.Exec(ctx, `INSERT INTO github_activity_state VALUES($1,$2,$3) ON CONFLICT(repository_id) DO UPDATE SET cursor_at=excluded.cursor_at,reconciled_at=excluded.reconciled_at`, id, cursor, full)
	return err
}
func (r *PostgresRepositoryStore) ReadActivity(ctx context.Context, project uuid.UUID, repo *uuid.UUID, kinds []string, n, limit, offset int) ([]json.RawMessage, error) {
	if repo == nil {
		if _, err := NewPostgresProjectRepository(r.pool).GetByID(ctx, project); err != nil {
			return nil, err
		}
	}
	rows, err := db.ConnFromContext(ctx, r.pool).Query(ctx, `SELECT a.data || jsonb_build_object('kind',a.kind,'repository_id',a.repository_id,'number',a.number)
 FROM github_activity a JOIN repositories r ON r.id=a.repository_id WHERE r.project_id=$1 AND ($2::uuid IS NULL OR r.id=$2) AND a.kind=ANY($3) AND ($4::int=0 OR a.number=$4) ORDER BY a.number DESC,a.repository_id,a.kind,a.native_id LIMIT $5 OFFSET $6`, project, repo, kinds, n, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if items == nil {
		items = []json.RawMessage{}
	}
	return items, err
}
func (r *PostgresRepositoryStore) ActivityParent(ctx context.Context, project, repo uuid.UUID, kind string, n int) (json.RawMessage, error) {
	items, err := r.ReadActivity(ctx, project, &repo, []string{kind}, n, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, domain.ErrNotFound
	}
	return items[0], nil
}
