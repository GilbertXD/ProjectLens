package domain

import (
	"time"

	"github.com/google/uuid"
)

type SnapshotStatus string

const (
	SnapshotStatusStaging    SnapshotStatus = "staging"
	SnapshotStatusActive     SnapshotStatus = "active"
	SnapshotStatusSuperseded SnapshotStatus = "superseded"
	SnapshotStatusFailed     SnapshotStatus = "failed"
)

type Snapshot struct {
	ID                   uuid.UUID      `json:"id"`
	RepositoryID         uuid.UUID      `json:"repository_id"`
	JobID                *uuid.UUID     `json:"job_id,omitempty"`
	ParentSnapshotID     *uuid.UUID     `json:"parent_snapshot_id,omitempty"`
	Version              int64          `json:"version"`
	Status               SnapshotStatus `json:"status"`
	ResolvedRef          string         `json:"resolved_ref"`
	CommitHash           string         `json:"commit_hash"`
	TreeHash             string         `json:"tree_hash"`
	ScannerConfigHash    string         `json:"scanner_config_hash"`
	CommitAuthorName     string         `json:"commit_author_name"`
	CommitAuthorEmail    string         `json:"commit_author_email"`
	CommitAuthoredAt     time.Time      `json:"commit_authored_at"`
	CommitCommitterName  string         `json:"commit_committer_name"`
	CommitCommitterEmail string         `json:"commit_committer_email"`
	CommitCommittedAt    time.Time      `json:"commit_committed_at"`
	CommitSubject        string         `json:"commit_subject"`
	CommitParents        []string       `json:"commit_parents"`
	TotalTreeEntries     int            `json:"total_tree_entries"`
	IncludedFiles        int            `json:"included_files"`
	IncludedBytes        int64          `json:"included_bytes"`
	SkippedFiles         int            `json:"skipped_files"`
	SkippedByReason      map[string]int `json:"skipped_by_reason"`
	Truncated            bool           `json:"truncated"`
	AddedFiles           int            `json:"added_files"`
	ModifiedFiles        int            `json:"modified_files"`
	DeletedFiles         int            `json:"deleted_files"`
	UnchangedFiles       int            `json:"unchanged_files"`
	BlobsRead            int            `json:"blobs_read"`
	ErrorMessage         *string        `json:"error_message,omitempty"`
	ScannedAt            time.Time      `json:"scanned_at"`
	PublishedAt          *time.Time     `json:"published_at,omitempty"`
	SupersededAt         *time.Time     `json:"superseded_at,omitempty"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

type SourceFile struct {
	ID           uuid.UUID `json:"id"`
	SnapshotID   uuid.UUID `json:"snapshot_id"`
	RepositoryID uuid.UUID `json:"repository_id"`
	Path         string    `json:"path"`
	BlobOID      string    `json:"blob_oid"`
	Mode         string    `json:"mode"`
	SizeBytes    int64     `json:"size_bytes"`
	SHA256       string    `json:"sha256"`
	LineCount    int       `json:"line_count"`
	IsExecutable bool      `json:"is_executable"`
	Content      *string   `json:"content,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type SyncOutcomeStatus string

const (
	SyncOutcomePublished SyncOutcomeStatus = "published"
	SyncOutcomeUnchanged SyncOutcomeStatus = "unchanged"
)

type SyncResult struct {
	Status          SyncOutcomeStatus `json:"status"`
	RepositoryID    uuid.UUID         `json:"repository_id"`
	SnapshotID      uuid.UUID         `json:"snapshot_id"`
	Version         int64             `json:"version"`
	CommitHash      string            `json:"commit_hash"`
	TreeHash        string            `json:"tree_hash"`
	BlobsRead       int               `json:"blobs_read"`
	AddedFiles      int               `json:"added_files"`
	ModifiedFiles   int               `json:"modified_files"`
	DeletedFiles    int               `json:"deleted_files"`
	UnchangedFiles  int               `json:"unchanged_files"`
	PrunedSnapshots int64             `json:"pruned_snapshots"`
}

type ListSnapshotsFilter struct {
	Limit  int
	Offset int
}

type SnapshotListResult struct {
	Items  []Snapshot `json:"items"`
	Total  int64      `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

type SearchFilesFilter struct {
	Query      string
	PathPrefix string
	Limit      int
	Offset     int
}

type SourceFileListResult struct {
	Items  []SourceFile `json:"items"`
	Total  int64        `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}
