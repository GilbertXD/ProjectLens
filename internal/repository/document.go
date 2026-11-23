package repository

import (
	"context"
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

const documentColumns = "id, project_id, title, content, content_bytes, version, created_at, updated_at"

func scanDocument(row pgx.Row) (domain.Document, error) {
	var doc domain.Document
	err := row.Scan(&doc.ID, &doc.ProjectID, &doc.Title, &doc.Content, &doc.ContentBytes, &doc.Version, &doc.CreatedAt, &doc.UpdatedAt)
	return doc, err
}

func (r *PostgresDocumentStore) Create(ctx context.Context, doc domain.Document) (domain.Document, error) {
	created, err := scanDocument(db.ConnFromContext(ctx, r.pool).QueryRow(ctx,
		"INSERT INTO documents (id, project_id, title, content, version) VALUES ($1, $2, $3, $4, 1) RETURNING "+documentColumns,
		doc.ID, doc.ProjectID, doc.Title, doc.Content))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return created, domain.ErrNotFound
	}
	return created, documentStoreError(err)
}

func (r *PostgresDocumentStore) GetByID(ctx context.Context, projectID, docID uuid.UUID) (domain.Document, error) {
	doc, err := scanDocument(db.ConnFromContext(ctx, r.pool).QueryRow(ctx,
		"SELECT "+documentColumns+" FROM documents WHERE project_id = $1 AND id = $2", projectID, docID))
	return doc, documentStoreError(err)
}

func (r *PostgresDocumentStore) ListByProject(ctx context.Context, projectID uuid.UUID, filter domain.ListDocumentsFilter) (domain.DocumentListResult, error) {
	result := domain.DocumentListResult{Items: []domain.DocumentSummary{}, Limit: filter.Limit, Offset: filter.Offset}
	rows, err := db.ConnFromContext(ctx, r.pool).Query(ctx, `
		WITH totals AS (SELECT count(*) AS total FROM documents WHERE project_id = $1),
		page AS (
			SELECT id, title, content_bytes, version, created_at, updated_at
			FROM documents WHERE project_id = $1
			ORDER BY updated_at DESC, id DESC LIMIT $2 OFFSET $3
		)
		SELECT page.id, page.title, page.content_bytes, page.version, page.created_at, page.updated_at, totals.total
		FROM totals LEFT JOIN page ON true ORDER BY page.updated_at DESC, page.id DESC`,
		projectID, int64(filter.Limit), int64(filter.Offset))
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id *uuid.UUID
		var title *string
		var contentBytes, version *int64
		var createdAt, updatedAt *time.Time
		if err := rows.Scan(&id, &title, &contentBytes, &version, &createdAt, &updatedAt, &result.Total); err != nil {
			return result, err
		}
		if id != nil {
			result.Items = append(result.Items, domain.DocumentSummary{
				ID: *id, ProjectID: projectID, Title: *title, ContentBytes: *contentBytes,
				Version: *version, CreatedAt: *createdAt, UpdatedAt: *updatedAt,
			})
		}
	}
	return result, rows.Err()
}

func (r *PostgresDocumentStore) Update(ctx context.Context, projectID, docID uuid.UUID, expectedVersion int64, title, content string) (domain.Document, error) {
	conn := db.ConnFromContext(ctx, r.pool)
	doc, err := scanDocument(conn.QueryRow(ctx, `
		UPDATE documents SET title = $4, content = $5, version = version + 1, updated_at = NOW()
		WHERE project_id = $1 AND id = $2 AND version = $3 RETURNING `+documentColumns,
		projectID, docID, expectedVersion, title, content))
	if errors.Is(err, pgx.ErrNoRows) {
		return doc, documentVersionMiss(ctx, conn, projectID, docID, expectedVersion)
	}
	return doc, documentStoreError(err)
}

func (r *PostgresDocumentStore) Patch(ctx context.Context, projectID, docID uuid.UUID, expectedVersion int64, title, content *string) (domain.Document, error) {
	conn := db.ConnFromContext(ctx, r.pool)
	doc, err := scanDocument(conn.QueryRow(ctx, `
		UPDATE documents SET title = COALESCE($4, title), content = COALESCE($5, content),
			version = version + 1, updated_at = NOW()
		WHERE project_id = $1 AND id = $2 AND version = $3 RETURNING `+documentColumns,
		projectID, docID, expectedVersion, title, content))
	if errors.Is(err, pgx.ErrNoRows) {
		return doc, documentVersionMiss(ctx, conn, projectID, docID, expectedVersion)
	}
	return doc, documentStoreError(err)
}

func (r *PostgresDocumentStore) Delete(ctx context.Context, projectID, docID uuid.UUID, expectedVersion *int64) error {
	conn := db.ConnFromContext(ctx, r.pool)
	query := "DELETE FROM documents WHERE project_id = $1 AND id = $2"
	args := []any{projectID, docID}
	if expectedVersion != nil {
		query += " AND version = $3"
		args = append(args, *expectedVersion)
	}
	tag, err := conn.Exec(ctx, query, args...)
	if err != nil || tag.RowsAffected() != 0 {
		return documentStoreError(err)
	}
	if expectedVersion == nil {
		return domain.ErrNotFound
	}
	return documentVersionMiss(ctx, conn, projectID, docID, *expectedVersion)
}

func documentVersionMiss(ctx context.Context, conn db.DBTX, projectID, docID uuid.UUID, expectedVersion int64) error {
	// A fresh statement sees the winning write at READ COMMITTED.
	var currentVersion int64
	err := conn.QueryRow(ctx, "SELECT version FROM documents WHERE project_id = $1 AND id = $2", projectID, docID).Scan(&currentVersion)
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
