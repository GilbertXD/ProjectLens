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

const repositoryColumns = "id, project_id, source_type, name, source, canonical_source, default_branch, description, remote_url, local_path, github_owner, github_repo, github_repo_id, github_visibility, github_stars, github_language, last_synced_at, created_at, updated_at"

func repositoryDestinations(repo *domain.Repository) []any {
	return []any{&repo.ID, &repo.ProjectID, &repo.SourceType, &repo.Name, &repo.Source, &repo.CanonicalSource,
		&repo.DefaultBranch, &repo.Description, &repo.RemoteURL, &repo.LocalPath, &repo.GitHubOwner, &repo.GitHubRepo,
		&repo.GitHubRepoID, &repo.GitHubVisibility, &repo.GitHubStars, &repo.GitHubLanguage, &repo.LastSyncedAt,
		&repo.CreatedAt, &repo.UpdatedAt}
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
  github_stars, github_language, last_synced_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
 RETURNING `+repositoryColumns, repo.ID, repo.ProjectID, repo.SourceType, repo.Name, repo.Source, repo.CanonicalSource,
		repo.DefaultBranch, repo.Description, repo.RemoteURL, repo.LocalPath, repo.GitHubOwner, repo.GitHubRepo,
		repo.GitHubRepoID, repo.GitHubVisibility, repo.GitHubStars, repo.GitHubLanguage, repo.LastSyncedAt))
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
  SELECT `+repositoryColumns+` FROM repositories WHERE project_id = $1 AND ($2 = '' OR source_type = $2)
 ), page AS (
  SELECT * FROM matching ORDER BY created_at DESC, id DESC LIMIT $3 OFFSET $4
 )
 SELECT page.*, totals.total
 FROM (SELECT count(*) AS total FROM matching) totals
 LEFT JOIN page ON true ORDER BY page.created_at DESC, page.id DESC`, projectID, filter.SourceType, int64(filter.Limit), int64(filter.Offset))
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
 UPDATE repositories SET name = COALESCE($3, name), description = COALESCE($4, description), updated_at = NOW()
 WHERE project_id = $1 AND id = $2 RETURNING `+repositoryColumns, projectID, repoID, input.Name, input.Description))
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
	tag, err := db.ConnFromContext(ctx, r.pool).Exec(ctx, "DELETE FROM repositories WHERE project_id = $1 AND id = $2", projectID, repoID)
	if err != nil {
		return repositoryError(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func repositoryError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return domain.ErrNotFound
	}
	return projectError(err)
}
