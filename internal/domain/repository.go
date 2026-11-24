package domain

import (
	"time"

	"github.com/google/uuid"
)

type RepositorySourceType string

const (
	RepositorySourceLocalGit RepositorySourceType = "local_git"
	RepositorySourceGitHub   RepositorySourceType = "github"
)

type RepositoryRole string

const (
	RepositoryRolePrimary   RepositoryRole = "primary"
	RepositoryRoleReference RepositoryRole = "reference"
)

func (r RepositoryRole) Valid() bool {
	return r == RepositoryRolePrimary || r == RepositoryRoleReference
}

type Repository struct {
	ID               uuid.UUID            `json:"id"`
	ProjectID        uuid.UUID            `json:"project_id"`
	Role             RepositoryRole       `json:"role"`
	SourceType       RepositorySourceType `json:"source_type"`
	Name             string               `json:"name"`
	Source           string               `json:"source"`
	CanonicalSource  string               `json:"canonical_source"`
	DefaultBranch    string               `json:"default_branch"`
	Description      string               `json:"description"`
	RemoteURL        string               `json:"remote_url,omitempty"`
	LocalPath        *string              `json:"local_path,omitempty"`
	GitHubOwner      *string              `json:"github_owner,omitempty"`
	GitHubRepo       *string              `json:"github_repo,omitempty"`
	GitHubRepoID     *int64               `json:"github_repo_id,omitempty"`
	GitHubVisibility *string              `json:"github_visibility,omitempty"`
	GitHubStars      *int                 `json:"github_stars,omitempty"`
	GitHubLanguage   *string              `json:"github_language,omitempty"`
	LastSyncedAt     *time.Time           `json:"last_synced_at,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

type AssociateRepositoryInput struct {
	Role        *RepositoryRole      `json:"role,omitempty"`
	SourceType  RepositorySourceType `json:"source_type"`
	Source      string               `json:"source"`
	Name        *string              `json:"name"`
	Description *string              `json:"description"`
}

type PatchRepositoryInput struct {
	Role        *RepositoryRole `json:"role,omitempty"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
}

type ListRepositoriesFilter struct {
	Role       RepositoryRole
	SourceType RepositorySourceType
	Limit      int
	Offset     int
}

type RepositoryListResult struct {
	Items  []Repository `json:"items"`
	Total  int64        `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}
