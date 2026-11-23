package domain

import (
	"time"

	"github.com/google/uuid"
)

type Document struct {
	ID           uuid.UUID `json:"id"`
	ProjectID    uuid.UUID `json:"project_id"`
	Title        string    `json:"title"`
	Content      string    `json:"content"`
	ContentBytes int64     `json:"content_bytes"`
	Version      int64     `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type DocumentSummary struct {
	ID           uuid.UUID `json:"id"`
	ProjectID    uuid.UUID `json:"project_id"`
	Title        string    `json:"title"`
	ContentBytes int64     `json:"content_bytes"`
	Version      int64     `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type CreateDocumentInput struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type UpdateDocumentInput struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Version int64  `json:"version"`
}

type PatchDocumentInput struct {
	Title   *string `json:"title"`
	Content *string `json:"content"`
	Version int64   `json:"version"`
}

type ListDocumentsFilter struct {
	Limit  int
	Offset int
}

type DocumentListResult struct {
	Items  []DocumentSummary `json:"items"`
	Total  int64             `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}
