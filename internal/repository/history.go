package repository

import (
	"context"
	"encoding/json"
	"errors"

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
	rows, err := r.pool.Query(ctx, "SELECT id FROM unnest($2::text[]) AS wanted(id) WHERE NOT EXISTS(SELECT 1 FROM repository_commits c WHERE c.repository_id=$1 AND c.sha=wanted.id)", id, shas)
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
func (r *PostgresRepositoryStore) StoreCommit(ctx context.Context, id uuid.UUID, item gitscan.HistoryCommit, changes gitscan.Changes) error {
	_, err := db.ConnFromContext(ctx, r.pool).Exec(ctx, "INSERT INTO repository_commits VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", id, item.Hash, item, changes)
	return err
}
func (r *PostgresRepositoryStore) PublishHistory(ctx context.Context, id uuid.UUID, ref string, page gitscan.HistoryPageResult) error {
	_, err := db.ConnFromContext(ctx, r.pool).Exec(ctx, `INSERT INTO repository_history_refs(repository_id,ref,tip,reachable,has_more) VALUES($1,$2,$3,$4,$5) ON CONFLICT(repository_id,ref) DO UPDATE SET tip=excluded.tip,reachable=excluded.reachable,has_more=excluded.has_more,synced_at=now()`, id, ref, page.Tip, page.Reachable, page.HasMore)
	return err
}
func (r *PostgresRepositoryStore) HistoryList(ctx context.Context, id uuid.UUID, ref string, limit, offset int) (json.RawMessage, error) {
	var raw json.RawMessage
	err := db.ConnFromContext(ctx, r.pool).QueryRow(ctx, `SELECT jsonb_build_object('ref',ref,'tip',tip,'has_more',has_more,'synced_at',synced_at,'total',cardinality(reachable),'items',coalesce((SELECT jsonb_agg(c.metadata ORDER BY s.pos) FROM unnest(reachable[$4::bigint+1:$4::bigint+$3::bigint]) WITH ORDINALITY s(sha,pos) JOIN repository_commits c ON c.sha=s.sha AND c.repository_id=h.repository_id),'[]')) FROM repository_history_refs h WHERE repository_id=$1 AND ref=$2`, id, ref, limit, min(offset, gitscan.HistoryLimit)).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		err = domain.ErrNotFound
	}
	return raw, err
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
