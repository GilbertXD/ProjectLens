package gitscan

import (
	"time"

	"projectlens/internal/domain"
)

type SourceSpec struct {
	depth         int
	compareBase   string
	requireCommit bool
	preferCache   bool
	deepenHistory bool
	SourceType    domain.RepositorySourceType
	Source        string
	Ref           string
}

type PreparedRepo struct {
	SourceType      domain.RepositorySourceType
	Source          string
	CanonicalSource string
	GitDir          string
	CommonDir       string
	PinnedCommit    string
	ResolvedRef     string
	Shallow         bool
	Release         func()
}

type CommitInfo struct {
	Hash           string    `json:"hash"`
	TreeHash       string    `json:"tree_hash"`
	Parents        []string  `json:"parents"`
	AuthorName     string    `json:"author_name"`
	AuthorEmail    string    `json:"author_email"`
	AuthoredAt     time.Time `json:"authored_at"`
	CommitterName  string    `json:"committer_name"`
	CommitterEmail string    `json:"committer_email"`
	CommittedAt    time.Time `json:"committed_at"`
	Subject        string    `json:"subject"`
}

type FileEntry struct {
	Path         string `json:"path"`
	BlobOID      string `json:"blob_oid"`
	Mode         string `json:"mode"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	LineCount    int    `json:"line_count"`
	IsExecutable bool   `json:"is_executable"`
	Content      []byte `json:"content,omitempty"`
}

type SkipReason string

const (
	SkipSymlink            SkipReason = "symlink"
	SkipSubmodule          SkipReason = "submodule"
	SkipUnsupportedEntry   SkipReason = "unsupported_entry"
	SkipUnsafePath         SkipReason = "unsafe_path"
	SkipPathLimitExceeded  SkipReason = "path_limit_exceeded"
	SkipIgnoredPath        SkipReason = "ignored_path"
	SkipFileTooLarge       SkipReason = "file_too_large"
	SkipBinaryFile         SkipReason = "binary_file"
	SkipMaxFilesExceeded   SkipReason = "max_files_exceeded"
	SkipTotalBytesExceeded SkipReason = "total_bytes_exceeded"
	SkipBlobReadError      SkipReason = "blob_read_error"
)

type SkippedFile struct {
	Path    string     `json:"path"`
	BlobOID string     `json:"blob_oid"`
	Mode    string     `json:"mode"`
	Size    int64      `json:"size"`
	Reason  SkipReason `json:"reason"`
}

type SnapshotStats struct {
	TotalTreeEntries     int            `json:"total_tree_entries"`
	IncludedFiles        int            `json:"included_files"`
	IncludedBytes        int64          `json:"included_bytes"`
	SkippedFiles         int            `json:"skipped_files"`
	SkippedByReason      map[string]int `json:"skipped_by_reason"`
	Truncated            bool           `json:"truncated"`
	SkippedListTruncated bool           `json:"skipped_list_truncated"`
}

type Snapshot struct {
	SourceType      domain.RepositorySourceType `json:"source_type"`
	CanonicalSource string                      `json:"canonical_source"`
	ResolvedRef     string                      `json:"resolved_ref"`
	Commit          CommitInfo                  `json:"commit"`
	Files           []FileEntry                 `json:"files"`
	Skipped         []SkippedFile               `json:"skipped"`
	Stats           SnapshotStats               `json:"stats"`
	ScannedAt       time.Time                   `json:"scanned_at"`
}

type ScanOptions struct {
	IncludeContent bool
}

type PreviousFile struct {
	Path      string
	BlobOID   string
	Mode      string
	Size      int64
	SHA256    string
	LineCount int
}

type IncrementalBase struct {
	ScannerConfigHash string
	CommitHash        string
	TreeHash          string
	Files             map[string]PreviousFile
}

type IncrementalFile struct {
	FileEntry
	Reused         bool
	ReusedFromPath string
}

type IncrementalScanResult struct {
	ResolvedRef       string
	Commit            CommitInfo
	ScannerConfigHash string
	ScannedAt         time.Time
	Stats             SnapshotStats
	Files             []IncrementalFile
	Skipped           []SkippedFile
	DeletedPaths      []string
	Unchanged         bool
	AddedFiles        int
	ModifiedFiles     int
	DeletedFiles      int
	UnchangedFiles    int
	BlobsRead         int
}
