package domain

import (
	"time"

	"github.com/google/uuid"
)

type KnowledgeScope string

const (
	KnowledgeScopeProject    KnowledgeScope = "project"
	KnowledgeScopeRepository KnowledgeScope = "repository"
)

func DocumentScope(repositoryID *uuid.UUID) KnowledgeScope {
	if repositoryID != nil {
		return KnowledgeScopeRepository
	}
	return KnowledgeScopeProject
}

// DocumentOrigin records the repository a document belonged to before that
// repository was disassociated. It is informational provenance only: it never
// grants repository scope or ownership.
type DocumentOrigin struct {
	RepositoryID   uuid.UUID `json:"repository_id"`
	RepositoryName string    `json:"repository_name"`
}

// OriginPatch describes an optional provenance change on a document write.
// A nil Origin with Clear unset leaves the stored origin untouched.
type OriginPatch struct {
	Origin *DocumentOrigin
	Clear  bool
}

type Document struct {
	ID           uuid.UUID       `json:"id"`
	ProjectID    uuid.UUID       `json:"project_id"`
	Scope        KnowledgeScope  `json:"scope"`
	RepositoryID *uuid.UUID      `json:"repository_id,omitempty"`
	Origin       *DocumentOrigin `json:"origin,omitempty"`
	Title        string          `json:"title"`
	Content      string          `json:"content"`
	ContentBytes int64           `json:"content_bytes"`
	Version      int64           `json:"version"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type DocumentSummary struct {
	ID           uuid.UUID       `json:"id"`
	ProjectID    uuid.UUID       `json:"project_id"`
	Scope        KnowledgeScope  `json:"scope"`
	RepositoryID *uuid.UUID      `json:"repository_id,omitempty"`
	Origin       *DocumentOrigin `json:"origin,omitempty"`
	Title        string          `json:"title"`
	ContentBytes int64           `json:"content_bytes"`
	Version      int64           `json:"version"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type CreateDocumentInput struct {
	RepositoryID *uuid.UUID `json:"repository_id,omitempty"`
	Title        string     `json:"title"`
	Content      string     `json:"content"`
}

type UpdateDocumentInput struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	Version int64  `json:"version"`
	// Origin replaces the stored provenance. A nil Origin clears it.
	Origin *DocumentOrigin `json:"origin,omitempty"`
}

type PatchDocumentInput struct {
	Title   *string `json:"title"`
	Content *string `json:"content"`
	Version int64   `json:"version"`
	// Origin sets the stored provenance when non-nil; ClearOrigin removes it.
	// Leaving both unset keeps the stored provenance unchanged.
	Origin      *DocumentOrigin `json:"origin,omitempty"`
	ClearOrigin bool            `json:"-"`
}

type ListDocumentsFilter struct {
	Scope        KnowledgeScope
	RepositoryID *uuid.UUID
	Limit        int
	Offset       int
}

type DocumentListResult struct {
	Items  []DocumentSummary `json:"items"`
	Total  int64             `json:"total"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}
