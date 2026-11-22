package domain

import (
	"time"

	"github.com/google/uuid"
)

type ProjectStatus string

const (
	ProjectStatusActive   ProjectStatus = "active"
	ProjectStatusArchived ProjectStatus = "archived"
)

type Project struct {
	ID          uuid.UUID     `json:"id"`
	Slug        string        `json:"slug"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Status      ProjectStatus `json:"status"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type ProjectAuditLog struct {
	ID          int64
	ProjectID   *uuid.UUID
	ProjectSlug string
	Action      string
	CreatedAt   time.Time
}

type CreateProjectInput struct {
	Slug        string        `json:"slug"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Status      ProjectStatus `json:"status"`
}

type UpdateProjectInput CreateProjectInput

type PatchProjectInput struct {
	Slug        *string        `json:"slug"`
	Name        *string        `json:"name"`
	Description *string        `json:"description"`
	Status      *ProjectStatus `json:"status"`
}

type ListProjectsFilter struct {
	Status ProjectStatus
	Limit  int
	Offset int
}

type ProjectListResult struct {
	Items  []Project `json:"items"`
	Total  int64     `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
}
