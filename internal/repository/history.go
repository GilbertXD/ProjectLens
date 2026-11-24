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
	"projectlens/internal/gitscan"
)

func (r *PostgresRepositoryStore) HistorySize(ctx context.Context, id uuid.UUID, ref string) (int, error) {
	var n int
	err := db.ConnFromContext(ctx, r.pool).QueryRow(ctx, "SELECT COALESCE((SELECT cardinality(reachable) FROM repository_history_refs WHERE repository_id=$1 AND ref=$2),0)", id, ref).Scan(&n)
	return n, err
}
func (r *PostgresRepositoryStore) MissingCommits(ctx context.Context, id uuid.UUID, shas []string) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT wanted.sha FROM unnest($2::text[]) AS wanted(sha)
		LEFT JOIN repository_commits c ON c.repository_id=$1 AND c.sha=wanted.sha
		WHERE c.sha IS NULL OR c.changes @> '{"parent_unavailable":true}'`, id, shas)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		out[sha] = true
	}
	return out, rows.Err()
}
func (r *PostgresRepositoryStore) StoreCommit(ctx context.Context, id uuid.UUID, item gitscan.HistoryCommit, changes gitscan.Changes) (bool, error) {
	conn := db.ConnFromContext(ctx, r.pool)
	insert, err := conn.Exec(ctx, "INSERT INTO repository_commits VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", id, item.Hash, item, changes)
	if err != nil || insert.RowsAffected() > 0 || changes.ParentUnavailable {
		return insert.RowsAffected() > 0, err
	}
	_, err = conn.Exec(ctx, `UPDATE repository_commits SET changes=$3
		WHERE repository_id=$1 AND sha=$2 AND changes @> '{"parent_unavailable":true}'`, id, item.Hash, changes)
	return false, err
}
func (r *PostgresRepositoryStore) PublishHistory(ctx context.Context, id uuid.UUID, ref string, page gitscan.HistoryPageResult) error {
	_, err := db.ConnFromContext(ctx, r.pool).Exec(ctx, `INSERT INTO repository_history_refs(repository_id,ref,tip,reachable,has_more) VALUES($1,$2,$3,$4,$5) ON CONFLICT(repository_id,ref) DO UPDATE SET tip=excluded.tip,reachable=excluded.reachable,has_more=excluded.has_more,synced_at=now()`, id, ref, page.Tip, page.Reachable, page.HasMore)
	return err
}
func (r *PostgresRepositoryStore) HistoryList(ctx context.Context, id uuid.UUID, ref string, limit, offset int) (json.RawMessage, error) {
	page := struct {
		Ref      string          `json:"ref"`
		Tip      string          `json:"tip"`
		HasMore  bool            `json:"has_more"`
		SyncedAt time.Time       `json:"synced_at"`
		Total    int             `json:"total"`
		Items    json.RawMessage `json:"items"`
		gitscan.HistoryWindow
	}{Ref: ref}
	err := db.ConnFromContext(ctx, r.pool).QueryRow(ctx, `
		SELECT tip, has_more, synced_at, cardinality(reachable), coalesce((
			SELECT jsonb_agg(c.metadata ORDER BY s.pos)
			FROM unnest(reachable[$4::bigint+1:$4::bigint+$3::bigint]) WITH ORDINALITY s(sha,pos)
			JOIN repository_commits c ON c.sha=s.sha AND c.repository_id=h.repository_id
		), '[]')
		FROM repository_history_refs h WHERE repository_id=$1 AND ref=$2`,
		id, ref, limit, min(offset, gitscan.HistoryLimit),
	).Scan(&page.Tip, &page.HasMore, &page.SyncedAt, &page.Total, &page.Items)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	page.HistoryWindow = gitscan.DescribeHistoryWindow(page.Total, page.HasMore)
	return json.Marshal(page)
}
func (r *PostgresRepositoryStore) Commit(ctx context.Context, id uuid.UUID, sha string, files bool, limit, offset int) (json.RawMessage, error) {
	col := "metadata"
	if files {
		col = `jsonb_build_object('truncated',changes->'truncated','total',jsonb_array_length(changes->'files'),
			'items',coalesce((SELECT jsonb_agg(f ORDER BY ord) FROM (
				SELECT f, ord FROM jsonb_array_elements(changes->'files') WITH ORDINALITY AS elem(f, ord)
				ORDER BY ord LIMIT $3 OFFSET $4
			) page),'[]'))`
	}
	args := []any{id, sha}
	if files {
		args = append(args, limit, offset)
	}
	var raw json.RawMessage
	err := db.ConnFromContext(ctx, r.pool).QueryRow(ctx, "SELECT "+col+" FROM repository_commits WHERE repository_id=$1 AND sha=$2", args...).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrNotFound
	}
	return raw, err
}
