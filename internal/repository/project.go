package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/db"
	"projectlens/internal/domain"
)

type PostgresProjectRepository struct{ pool *pgxpool.Pool }

func NewPostgresProjectRepository(pool *pgxpool.Pool) *PostgresProjectRepository {
	return &PostgresProjectRepository{pool: pool}
}

const projectColumns = "id, slug, name, description, status, created_at, updated_at"

func scanProject(row pgx.Row) (domain.Project, error) {
	var p domain.Project
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.Description, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	return p, projectError(err)
}

func (r *PostgresProjectRepository) Create(ctx context.Context, id uuid.UUID, input domain.CreateProjectInput) (domain.Project, error) {
	return scanProject(db.ConnFromContext(ctx, r.pool).QueryRow(ctx,
		"INSERT INTO projects (id, slug, name, description, status) VALUES ($1, $2, $3, $4, $5) RETURNING "+projectColumns,
		id, input.Slug, input.Name, input.Description, input.Status))
}

func (r *PostgresProjectRepository) GetByID(ctx context.Context, id uuid.UUID) (domain.Project, error) {
	return scanProject(db.ConnFromContext(ctx, r.pool).QueryRow(ctx, "SELECT "+projectColumns+" FROM projects WHERE id = $1", id))
}

func (r *PostgresProjectRepository) List(ctx context.Context, filter domain.ListProjectsFilter) (domain.ProjectListResult, error) {
	result := domain.ProjectListResult{Items: []domain.Project{}, Limit: filter.Limit, Offset: filter.Offset}
	// The left join preserves the count when the requested page has no rows.
	rows, err := db.ConnFromContext(ctx, r.pool).Query(ctx, `
  SELECT page.id, page.slug, page.name, page.description, page.status, page.created_at, page.updated_at, totals.total
  FROM (SELECT count(*) AS total FROM projects WHERE ($1 = '' OR status = $1)) AS totals
  LEFT JOIN (
   SELECT id, slug, name, description, status, created_at, updated_at
   FROM projects WHERE ($1 = '' OR status = $1)
   ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3
  ) AS page ON true
  ORDER BY page.created_at DESC, page.id DESC`, filter.Status, int64(filter.Limit), int64(filter.Offset))
	if err != nil {
		return result, projectError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id *uuid.UUID
		// Nullable destinations also handle the count-only row.
		var slug, name, description *string
		var status *domain.ProjectStatus
		var created, updated *time.Time
		if err := rows.Scan(&id, &slug, &name, &description, &status, &created, &updated, &result.Total); err != nil {
			return result, projectError(err)
		}
		if id != nil {
			result.Items = append(result.Items, domain.Project{ID: *id, Slug: *slug, Name: *name, Description: *description, Status: *status, CreatedAt: *created, UpdatedAt: *updated})
		}
	}
	return result, projectError(rows.Err())
}

func (r *PostgresProjectRepository) Update(ctx context.Context, id uuid.UUID, input domain.UpdateProjectInput) (domain.Project, error) {
	return r.Patch(ctx, id, domain.PatchProjectInput{Slug: &input.Slug, Name: &input.Name, Description: &input.Description, Status: &input.Status})
}

func (r *PostgresProjectRepository) Patch(ctx context.Context, id uuid.UUID, input domain.PatchProjectInput) (domain.Project, error) {
	return scanProject(db.ConnFromContext(ctx, r.pool).QueryRow(ctx, `
  UPDATE projects SET slug = COALESCE($2, slug), name = COALESCE($3, name),
   description = COALESCE($4, description), status = COALESCE($5, status), updated_at = NOW()
  WHERE id = $1 RETURNING `+projectColumns, id, input.Slug, input.Name, input.Description, input.Status))
}

func (r *PostgresProjectRepository) Delete(ctx context.Context, id uuid.UUID) (domain.Project, error) {
	return scanProject(db.ConnFromContext(ctx, r.pool).QueryRow(ctx, "DELETE FROM projects WHERE id = $1 RETURNING "+projectColumns, id))
}

func (r *PostgresProjectRepository) InsertAuditLog(ctx context.Context, entry domain.ProjectAuditLog) error {
	_, err := db.ConnFromContext(ctx, r.pool).Exec(ctx,
		"INSERT INTO project_audit_logs (project_id, project_slug, action) VALUES ($1, $2, $3)", entry.ProjectID, entry.ProjectSlug, entry.Action)
	return projectError(err)
}

func (r *PostgresProjectRepository) ListAuditLogsBySlug(ctx context.Context, slug string) ([]domain.ProjectAuditLog, error) {
	rows, err := db.ConnFromContext(ctx, r.pool).Query(ctx,
		"SELECT id, project_id, project_slug, action, created_at FROM project_audit_logs WHERE project_slug = $1 ORDER BY created_at DESC, id DESC", slug)
	if err != nil {
		return nil, projectError(err)
	}
	logs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[domain.ProjectAuditLog])
	return logs, projectError(err)
}

func projectError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return domain.ErrConflict
		case "23514", "23502", "22P02":
			return domain.ErrInvalidInput
		}
	}
	return err
}
