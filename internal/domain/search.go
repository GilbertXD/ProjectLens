package domain

import (
	"time"

	"github.com/google/uuid"
)

type SearchHitKind string
type SearchSourceType string
type SearchCacheStatus string

const (
	SearchHitKindSourceFile SearchHitKind = "source_file"
	SearchHitKindDocument   SearchHitKind = "document"

	SearchSourceLocalGit SearchSourceType = "local_git"
	SearchSourceGitHub   SearchSourceType = "github"
	SearchSourceDocument SearchSourceType = "document"

	SearchCacheStatusHit      SearchCacheStatus = "hit"
	SearchCacheStatusMiss     SearchCacheStatus = "miss"
	SearchCacheStatusBypass   SearchCacheStatus = "bypass"
	SearchCacheStatusDegraded SearchCacheStatus = "degraded"
)

type SearchFilter struct {
	Query        string
	SourceTypes  []SearchSourceType
	RepositoryID *uuid.UUID
	PathPrefix   string
	Limit        int
	Offset       int
}

type SearchLocation struct {
	RepositoryID     *uuid.UUID `json:"repository_id,omitempty"`
	RepositoryName   string     `json:"repository_name,omitempty"`
	RepositorySource string     `json:"repository_source,omitempty"`
	SnapshotID       *uuid.UUID `json:"snapshot_id,omitempty"`
	SnapshotVersion  *int64     `json:"snapshot_version,omitempty"`
	CommitHash       string     `json:"commit_hash,omitempty"`
	Path             string     `json:"path,omitempty"`
	URL              string     `json:"url,omitempty"`
	DocumentID       *uuid.UUID `json:"document_id,omitempty"`
	DocumentTitle    string     `json:"document_title,omitempty"`
	DocumentVersion  *int64     `json:"document_version,omitempty"`
	Line             *int       `json:"line,omitempty"`
	APIPath          string     `json:"api_path"`
}

type SearchHit struct {
	Kind       SearchHitKind    `json:"kind"`
	SourceType SearchSourceType `json:"source_type"`
	ID         uuid.UUID        `json:"id"`
	ProjectID  uuid.UUID        `json:"project_id"`
	Title      string           `json:"title"`
	Snippet    string           `json:"snippet"`
	Score      float64          `json:"score"`
	UpdatedAt  time.Time        `json:"updated_at"`
	Location   SearchLocation   `json:"location"`
}

type SearchResult struct {
	Query         string            `json:"query"`
	Items         []SearchHit       `json:"items"`
	Total         int64             `json:"total"`
	Limit         int               `json:"limit"`
	Offset        int               `json:"offset"`
	SearchVersion int64             `json:"search_version"`
	CacheStatus   SearchCacheStatus `json:"cache_status,omitempty"`
}
