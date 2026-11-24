package domain

import (
	"time"

	"github.com/google/uuid"
)

type SearchHitKind string
type SearchSourceType string
type SearchCacheStatus string
type SearchScope string

const (
	SearchScopeAll             SearchScope = "all"
	SearchScopeProject         SearchScope = "project"
	SearchScopeRepository      SearchScope = "repository"
	SearchScopeCrossRepository SearchScope = "cross_repository"

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
	Scope                   SearchScope
	RepositoryIDs           []uuid.UUID
	RepositoryRole          RepositoryRole
	IncludeProjectDocuments bool
	Query                   string
	SourceTypes             []SearchSourceType
	RepositoryID            *uuid.UUID
	PathPrefix              string
	Limit                   int
	Offset                  int
}

// EffectiveScope resolves the shorthand repository filters.
func (f SearchFilter) EffectiveScope() SearchScope {
	if f.Scope != "" && f.Scope != SearchScopeAll {
		return f.Scope
	}
	if f.RepositoryID != nil {
		return SearchScopeRepository
	}
	if len(f.RepositoryIDs) > 0 || f.RepositoryRole != "" {
		return SearchScopeCrossRepository
	}
	return SearchScopeAll
}

type SearchLocation struct {
	Scope            KnowledgeScope `json:"scope,omitempty"`
	RepositoryRole   RepositoryRole `json:"repository_role,omitempty"`
	RepositoryID     *uuid.UUID     `json:"repository_id,omitempty"`
	RepositoryName   string         `json:"repository_name,omitempty"`
	RepositorySource string         `json:"repository_source,omitempty"`
	SnapshotID       *uuid.UUID     `json:"snapshot_id,omitempty"`
	SnapshotVersion  *int64         `json:"snapshot_version,omitempty"`
	CommitHash       string         `json:"commit_hash,omitempty"`
	Path             string         `json:"path,omitempty"`
	URL              string         `json:"url,omitempty"`
	DocumentID       *uuid.UUID     `json:"document_id,omitempty"`
	DocumentTitle    string         `json:"document_title,omitempty"`
	DocumentVersion  *int64         `json:"document_version,omitempty"`
	Line             *int           `json:"line,omitempty"`
	APIPath          string         `json:"api_path"`
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
