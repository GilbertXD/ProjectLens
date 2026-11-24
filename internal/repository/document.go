package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/db"
	"projectlens/internal/domain"
)

type PostgresDocumentStore struct{ pool *pgxpool.Pool }

func NewPostgresDocumentStore(pool *pgxpool.Pool) *PostgresDocumentStore {
	return &PostgresDocumentStore{pool: pool}
}

const documentColumns = "id, project_id, title, content, content_bytes, version, created_at, updated_at, repository_id, metadata"

func scanDocument(row pgx.Row) (domain.Document, error) {
	var doc domain.Document
	var metadata []byte
	err := row.Scan(&doc.ID, &doc.ProjectID, &doc.Title, &doc.Content, &doc.ContentBytes, &doc.Version, &doc.CreatedAt, &doc.UpdatedAt, &doc.RepositoryID, &metadata)
	doc.Scope = domain.DocumentScope(doc.RepositoryID)
	doc.Origin = documentOrigin(metadata)
	return doc, err
}

// documentOrigin decodes the informational provenance kept in documents.metadata.
func documentOrigin(metadata []byte) *domain.DocumentOrigin {
	if len(metadata) == 0 {
		return nil
	}
	var stored struct {
		Origin *domain.DocumentOrigin `json:"origin"`
	}
	if err := json.Unmarshal(metadata, &stored); err != nil {
		return nil
	}
	return stored.Origin
}

func (r *PostgresDocumentStore) Create(ctx context.Context, doc domain.Document) (domain.Document, error) {
	created, err := scanDocument(db.ConnFromContext(ctx, r.pool).QueryRow(ctx,
		"INSERT INTO documents (id, project_id, title, content, version, repository_id) VALUES ($1, $2, $3, $4, 1, $5) RETURNING "+documentColumns,
		doc.ID, doc.ProjectID, doc.Title, doc.Content, doc.RepositoryID))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return created, domain.ErrNotFound
	}
	return created, documentStoreError(err)
}

func (r *PostgresDocumentStore) GetByID(ctx context.Context, projectID, docID uuid.UUID) (domain.Document, error) {
	return r.GetScopedByID(ctx, projectID, docID, nil)
}

func (r *PostgresDocumentStore) GetScopedByID(ctx context.Context, projectID, docID uuid.UUID, repositoryID *uuid.UUID) (domain.Document, error) {
	doc, err := scanDocument(db.ConnFromContext(ctx, r.pool).QueryRow(ctx,
		"SELECT "+documentColumns+" FROM documents WHERE project_id = $1 AND id = $2 AND ($3::uuid IS NULL OR repository_id = $3)", projectID, docID, repositoryID))
	return doc, documentStoreError(err)
}

func (r *PostgresDocumentStore) ListByProject(ctx context.Context, projectID uuid.UUID, filter domain.ListDocumentsFilter) (domain.DocumentListResult, error) {
	result := domain.DocumentListResult{Items: []domain.DocumentSummary{}, Limit: filter.Limit, Offset: filter.Offset}
	rows, err := db.ConnFromContext(ctx, r.pool).Query(ctx, `
		WITH matching AS NOT MATERIALIZED (
			SELECT id, title, content_bytes, version, created_at, updated_at, repository_id, metadata
			FROM documents WHERE project_id = $1
				AND ($4::uuid IS NULL OR repository_id = $4)
				AND ($5::text <> 'project' OR repository_id IS NULL)
				AND ($5::text <> 'repository' OR repository_id IS NOT NULL)
		), totals AS (SELECT count(*) AS total FROM matching),
		page AS (SELECT * FROM matching ORDER BY updated_at DESC, id DESC LIMIT $2 OFFSET $3)
		SELECT page.id, page.title, page.content_bytes, page.version, page.created_at, page.updated_at,
			page.repository_id, page.metadata, totals.total
		FROM totals LEFT JOIN page ON true
		WHERE $4::uuid IS NULL OR EXISTS (SELECT 1 FROM repositories WHERE project_id = $1 AND id = $4)
		ORDER BY page.updated_at DESC, page.id DESC`,
		projectID, int64(filter.Limit), int64(filter.Offset), filter.RepositoryID, filter.Scope)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		found = true
		var id, repositoryID *uuid.UUID
		var title *string
		var contentBytes, version *int64
		var createdAt, updatedAt *time.Time
		var metadata []byte
		if err := rows.Scan(&id, &title, &contentBytes, &version, &createdAt, &updatedAt, &repositoryID, &metadata, &result.Total); err != nil {
			return result, err
		}
		if id != nil {
			result.Items = append(result.Items, domain.DocumentSummary{
				ID: *id, ProjectID: projectID, RepositoryID: repositoryID, Scope: domain.DocumentScope(repositoryID), Title: *title, ContentBytes: *contentBytes,
				Version: *version, CreatedAt: *createdAt, UpdatedAt: *updatedAt, Origin: documentOrigin(metadata),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if !found {
		return result, domain.ErrNotFound
	}
	return result, nil
}

func (r *PostgresDocumentStore) Update(ctx context.Context, projectID, docID uuid.UUID, repositoryID *uuid.UUID, expectedVersion int64, title, content string, origin domain.OriginPatch) (domain.Document, error) {
	return r.Patch(ctx, projectID, docID, repositoryID, expectedVersion, &title, &content, origin)
}

func (r *PostgresDocumentStore) Patch(ctx context.Context, projectID, docID uuid.UUID, repositoryID *uuid.UUID, expectedVersion int64, title, content *string, origin domain.OriginPatch) (domain.Document, error) {
	conn := db.ConnFromContext(ctx, r.pool)
	var originJSON *string
	if origin.Origin != nil {
		encoded, err := json.Marshal(origin.Origin)
		if err != nil {
			return domain.Document{}, err
		}
		value := string(encoded)
		originJSON = &value
	}
	doc, err := scanDocument(conn.QueryRow(ctx, `
		UPDATE documents SET title = COALESCE($4, title), content = COALESCE($5, content),
			metadata = CASE
				WHEN $7::jsonb IS NOT NULL THEN jsonb_set(metadata, '{origin}', $7::jsonb)
				WHEN $8::boolean THEN metadata - 'origin'
				ELSE metadata END,
			version = version + 1, updated_at = NOW()
		WHERE project_id = $1 AND id = $2 AND version = $3 AND ($6::uuid IS NULL OR repository_id = $6) RETURNING `+documentColumns,
		projectID, docID, expectedVersion, title, content, repositoryID, originJSON, origin.Clear))
	if errors.Is(err, pgx.ErrNoRows) {
		return doc, documentVersionMiss(ctx, conn, projectID, docID, repositoryID, expectedVersion)
	}
	return doc, documentStoreError(err)
}

func (r *PostgresDocumentStore) Delete(ctx context.Context, projectID, docID uuid.UUID, repositoryID *uuid.UUID, expectedVersion *int64) error {
	conn := db.ConnFromContext(ctx, r.pool)
	query := "DELETE FROM documents WHERE project_id = $1 AND id = $2 AND ($3::uuid IS NULL OR repository_id = $3)"
	args := []any{projectID, docID, repositoryID}
	if expectedVersion != nil {
		query += " AND version = $4"
		args = append(args, *expectedVersion)
	}
	tag, err := conn.Exec(ctx, query, args...)
	if err != nil || tag.RowsAffected() != 0 {
		return documentStoreError(err)
	}
	if expectedVersion == nil {
		return domain.ErrNotFound
	}
	return documentVersionMiss(ctx, conn, projectID, docID, repositoryID, *expectedVersion)
}

func documentVersionMiss(ctx context.Context, conn db.DBTX, projectID, docID uuid.UUID, repositoryID *uuid.UUID, expectedVersion int64) error {
	// A fresh statement sees the winning write at READ COMMITTED.
	var currentVersion int64
	err := conn.QueryRow(ctx, "SELECT version FROM documents WHERE project_id = $1 AND id = $2 AND ($3::uuid IS NULL OR repository_id = $3)", projectID, docID, repositoryID).Scan(&currentVersion)
	if err != nil {
		return documentStoreError(err)
	}
	return fmt.Errorf("%w: expected version %d, current version %d", domain.ErrVersionConflict, expectedVersion, currentVersion)
}

func documentStoreError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23514", "23502", "22P02":
			return domain.ErrInvalidInput
		}
	}
	return err
}
