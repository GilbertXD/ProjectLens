package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/db"
	"projectlens/internal/domain"
)

type PostgresRepositoryStore struct {
	pool *pgxpool.Pool
}

func NewPostgresRepositoryStore(pool *pgxpool.Pool) *PostgresRepositoryStore {
	return &PostgresRepositoryStore{pool: pool}
}

const repositoryColumns = "id, project_id, source_type, name, source, canonical_source, default_branch, description, remote_url, local_path, github_owner, github_repo, github_repo_id, github_visibility, github_stars, github_language, last_synced_at, created_at, updated_at, role"

func repositoryDestinations(repo *domain.Repository) []any {
	return []any{&repo.ID, &repo.ProjectID, &repo.SourceType, &repo.Name, &repo.Source, &repo.CanonicalSource,
		&repo.DefaultBranch, &repo.Description, &repo.RemoteURL, &repo.LocalPath, &repo.GitHubOwner, &repo.GitHubRepo,
		&repo.GitHubRepoID, &repo.GitHubVisibility, &repo.GitHubStars, &repo.GitHubLanguage, &repo.LastSyncedAt,
		&repo.CreatedAt, &repo.UpdatedAt, &repo.Role}
}

func scanRepository(row pgx.Row) (domain.Repository, error) {
	var repo domain.Repository
	err := row.Scan(repositoryDestinations(&repo)...)
	return repo, repositoryError(err)
}

func (r *PostgresRepositoryStore) Create(ctx context.Context, repo domain.Repository) (domain.Repository, error) {
	return scanRepository(db.ConnFromContext(ctx, r.pool).QueryRow(ctx, `
 INSERT INTO repositories (id, project_id, source_type, name, source, canonical_source, default_branch,
  description, remote_url, local_path, github_owner, github_repo, github_repo_id, github_visibility,
  github_stars, github_language, last_synced_at, role)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,COALESCE(NULLIF($18, ''), 'primary'))
 RETURNING `+repositoryColumns, repo.ID, repo.ProjectID, repo.SourceType, repo.Name, repo.Source, repo.CanonicalSource,
		repo.DefaultBranch, repo.Description, repo.RemoteURL, repo.LocalPath, repo.GitHubOwner, repo.GitHubRepo,
		repo.GitHubRepoID, repo.GitHubVisibility, repo.GitHubStars, repo.GitHubLanguage, repo.LastSyncedAt, repo.Role))
}

func (r *PostgresRepositoryStore) GetByID(ctx context.Context, projectID, repoID uuid.UUID) (domain.Repository, error) {
	return scanRepository(db.ConnFromContext(ctx, r.pool).QueryRow(ctx,
		"SELECT "+repositoryColumns+" FROM repositories WHERE project_id = $1 AND id = $2", projectID, repoID))
}

func (r *PostgresRepositoryStore) GetByCanonicalSource(ctx context.Context, projectID uuid.UUID, canonicalSource string) (domain.Repository, error) {
	return scanRepository(db.ConnFromContext(ctx, r.pool).QueryRow(ctx,
		"SELECT "+repositoryColumns+" FROM repositories WHERE project_id = $1 AND canonical_source = $2", projectID, canonicalSource))
}

func (r *PostgresRepositoryStore) ListByProject(ctx context.Context, projectID uuid.UUID, filter domain.ListRepositoriesFilter) (domain.RepositoryListResult, error) {
	result := domain.RepositoryListResult{Items: []domain.Repository{}, Limit: filter.Limit, Offset: filter.Offset}
	// The count-only row preserves total on empty pages. The two reads share a statement snapshot.
	rows, err := db.ConnFromContext(ctx, r.pool).Query(ctx, `
 WITH matching AS NOT MATERIALIZED (
  SELECT `+repositoryColumns+` FROM repositories WHERE project_id = $1 AND ($2 = '' OR source_type = $2) AND ($5 = '' OR role = $5)
 ), page AS (
  SELECT * FROM matching ORDER BY created_at DESC, id DESC LIMIT $3 OFFSET $4
 )
 SELECT page.*, totals.total
 FROM (SELECT count(*) AS total FROM matching) totals
 LEFT JOIN page ON true ORDER BY page.created_at DESC, page.id DESC`, projectID, filter.SourceType, int64(filter.Limit), int64(filter.Offset), filter.Role)
	if err != nil {
		return result, repositoryError(err)
	}
	defer rows.Close()
	for rows.Next() {
		if rows.RawValues()[0] == nil {
			// Scan only the count on the row produced by the outer join.
			dest := make([]any, len(rows.RawValues()))
			dest[len(dest)-1] = &result.Total
			if err := rows.Scan(dest...); err != nil {
				return result, repositoryError(err)
			}
			continue
		}
		var repo domain.Repository
		if err := rows.Scan(append(repositoryDestinations(&repo), &result.Total)...); err != nil {
			return result, repositoryError(err)
		}
		result.Items = append(result.Items, repo)
	}
	return result, repositoryError(rows.Err())
}

func (r *PostgresRepositoryStore) Patch(ctx context.Context, projectID, repoID uuid.UUID, input domain.PatchRepositoryInput) (domain.Repository, error) {
	return scanRepository(db.ConnFromContext(ctx, r.pool).QueryRow(ctx, `
 UPDATE repositories SET name = COALESCE($3, name), description = COALESCE($4, description), role = COALESCE($5, role), updated_at = NOW()
 WHERE project_id = $1 AND id = $2 RETURNING `+repositoryColumns, projectID, repoID, input.Name, input.Description, input.Role))
}

func (r *PostgresRepositoryStore) UpdateMetadata(ctx context.Context, repo domain.Repository) (domain.Repository, error) {
	return scanRepository(db.ConnFromContext(ctx, r.pool).QueryRow(ctx, `
 UPDATE repositories SET canonical_source = $3, default_branch = $4, remote_url = $5, local_path = $6,
  github_owner = $7, github_repo = $8, github_repo_id = $9, github_visibility = $10,
  github_stars = $11, github_language = $12, last_synced_at = $13, updated_at = NOW()
 WHERE project_id = $1 AND id = $2 RETURNING `+repositoryColumns, repo.ProjectID, repo.ID,
		repo.CanonicalSource, repo.DefaultBranch, repo.RemoteURL, repo.LocalPath, repo.GitHubOwner, repo.GitHubRepo,
		repo.GitHubRepoID, repo.GitHubVisibility, repo.GitHubStars, repo.GitHubLanguage, repo.LastSyncedAt))
}

func (r *PostgresRepositoryStore) Delete(ctx context.Context, projectID, repoID uuid.UUID) error {
	err := db.NewPgxTxManager(r.pool).WithinTx(ctx, func(ctx context.Context) error {
		conn := db.ConnFromContext(ctx, r.pool)
		// Lock the repository row first. Foreign-key checks on document writes
		// hold FOR KEY SHARE on this row, so this lock serialises concurrent
		// document creation and re-association against disassociation instead of
		// letting a late document escape without provenance.
		var name string
		err := conn.QueryRow(ctx, "SELECT name FROM repositories WHERE project_id = $1 AND id = $2 FOR UPDATE", projectID, repoID).Scan(&name)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		// Documents are user-authored notes, so they survive as project-level
		// documents that keep the repository as read-only provenance. The version
		// bump and fresh updated_at invalidate ETags held by clients that saw the
		// repository scope, so a stale write cannot silently overwrite the shift.
		if _, err := conn.Exec(ctx, `
			UPDATE documents SET metadata = jsonb_set(metadata, '{origin}',
				jsonb_build_object('repository_id', $2::text, 'repository_name', $3)),
				version = version + 1, updated_at = NOW()
			WHERE project_id = $1 AND repository_id = $2`, projectID, repoID, name); err != nil {
			return err
		}
		// Snapshots, source files, history, GitHub activity, and sync policies
		// cascade from the repository row, and the same delete clears the scope of
		// surviving documents. The bump_repository_search_version trigger still
		// invalidates the project's search cache.
		if _, err := conn.Exec(ctx, "DELETE FROM repositories WHERE project_id = $1 AND id = $2", projectID, repoID); err != nil {
			return err
		}
		_, err = conn.Exec(ctx, `
			UPDATE jobs SET
				status = CASE WHEN status = 'pending' THEN 'cancelled' ELSE status END,
				finished_at = CASE WHEN status = 'pending' THEN NOW() ELSE finished_at END,
				last_error_code = CASE WHEN status = 'pending' THEN 'cancelled' ELSE last_error_code END,
				last_error = CASE WHEN status = 'pending' THEN 'repository disassociated' ELSE last_error END,
				cancel_requested_at = CASE WHEN status = 'running' THEN COALESCE(cancel_requested_at, NOW()) ELSE cancel_requested_at END,
				updated_at = NOW()
			WHERE lower(payload->>'repository_id') = $1 AND status IN ('pending', 'running')`, repoID.String())
		return err
	})
	return repositoryError(err)
}

func repositoryError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return domain.ErrNotFound
	}
	return projectError(err)
}
