package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/gitscan"
	"projectlens/internal/repository"
)

type syncRepositoryLookup interface {
	GetByID(context.Context, uuid.UUID, uuid.UUID) (domain.Repository, error)
}

type snapshotStore interface {
	AcquireRepoSyncLock(context.Context, uuid.UUID) (func(), error)
	GetActiveSnapshot(context.Context, uuid.UUID) (domain.Snapshot, error)
	ListFileMetadataBySnapshot(context.Context, uuid.UUID) ([]domain.SourceFile, error)
	TouchRepositorySyncedAt(context.Context, uuid.UUID, uuid.UUID) error
	PublishSnapshot(context.Context, db.TxManager, repository.PublishSnapshotRequest) (domain.Snapshot, error)
	PruneSnapshots(context.Context, uuid.UUID, int, time.Time) (int64, error)
	ListSnapshots(context.Context, uuid.UUID, uuid.UUID, domain.ListSnapshotsFilter) (domain.SnapshotListResult, error)
	GetActiveSnapshotByProject(context.Context, uuid.UUID, uuid.UUID) (domain.Snapshot, error)
	SearchActiveFiles(context.Context, uuid.UUID, uuid.UUID, domain.SearchFilesFilter) (domain.SourceFileListResult, error)
	GetActiveFile(context.Context, uuid.UUID, uuid.UUID, string) (domain.SourceFile, error)
}

type incrementalScanner interface {
	ScanIncremental(context.Context, gitscan.SourceSpec, *gitscan.IncrementalBase, gitscan.ScanOptions) (*gitscan.IncrementalScanResult, error)
}

type SyncService struct {
	repos   syncRepositoryLookup
	store   snapshotStore
	tx      db.TxManager
	scanner incrementalScanner
	cfg     config.SyncConfig
	log     *slog.Logger
}

func NewSyncService(repos syncRepositoryLookup, store snapshotStore, tx db.TxManager, scanner incrementalScanner, cfg config.SyncConfig, log *slog.Logger) *SyncService {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &SyncService{repos: repos, store: store, tx: tx, scanner: scanner, cfg: cfg, log: log}
}

func (s *SyncService) SyncRepository(ctx context.Context, projectID, repoID uuid.UUID, ref string, jobID *uuid.UUID) (domain.SyncResult, error) {
	if projectID == uuid.Nil || repoID == uuid.Nil {
		return domain.SyncResult{}, fmt.Errorf("%w: project_id and repository_id are required", domain.ErrInvalidInput)
	}
	release, err := s.store.AcquireRepoSyncLock(ctx, repoID)
	if err != nil {
		return domain.SyncResult{}, err
	}
	defer release()

	repo, err := s.repos.GetByID(ctx, projectID, repoID)
	if err != nil {
		return domain.SyncResult{}, err
	}
	source := repo.Source
	switch repo.SourceType {
	case domain.RepositorySourceLocalGit:
		if repo.LocalPath != nil && *repo.LocalPath != "" {
			source = *repo.LocalPath
		}
	case domain.RepositorySourceGitHub:
		if repo.RemoteURL != "" {
			source = repo.RemoteURL
		}
	}
	active, base, err := s.incrementalBase(ctx, repoID)
	if err != nil {
		return domain.SyncResult{}, err
	}
	scan, err := s.scanner.ScanIncremental(ctx, gitscan.SourceSpec{
		SourceType: repo.SourceType, Source: source, Ref: ref,
	}, base, gitscan.ScanOptions{IncludeContent: true})
	if err != nil {
		return domain.SyncResult{}, err
	}

	status := domain.SyncOutcomeUnchanged
	if scan.Unchanged {
		if err := s.store.TouchRepositorySyncedAt(ctx, projectID, repoID); err != nil {
			return domain.SyncResult{}, err
		}
	} else {
		req := newPublishRequest(projectID, repoID, jobID, scan)
		if base != nil {
			parentID := active.ID
			req.ExpectedParentSnapshotID = &parentID
		}
		active, err = s.store.PublishSnapshot(ctx, s.tx, req)
		if err != nil {
			return domain.SyncResult{}, err
		}
		status = domain.SyncOutcomePublished
	}

	pruned, err := s.store.PruneSnapshots(ctx, repoID, s.cfg.RetainSuperseded, time.Now().UTC().Add(-s.cfg.FailedRetention))
	if err != nil {
		s.log.WarnContext(ctx, "prune repository snapshots", "repository_id", repoID, "error", err)
	}
	return domain.SyncResult{
		Status: status, RepositoryID: repoID, SnapshotID: active.ID, Version: active.Version,
		CommitHash: active.CommitHash, TreeHash: active.TreeHash, BlobsRead: scan.BlobsRead,
		AddedFiles: scan.AddedFiles, ModifiedFiles: scan.ModifiedFiles, DeletedFiles: scan.DeletedFiles,
		UnchangedFiles: scan.UnchangedFiles, PrunedSnapshots: pruned,
	}, nil
}

func (s *SyncService) incrementalBase(ctx context.Context, repoID uuid.UUID) (domain.Snapshot, *gitscan.IncrementalBase, error) {
	active, err := s.store.GetActiveSnapshot(ctx, repoID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Snapshot{}, nil, nil
	}
	if err != nil {
		return domain.Snapshot{}, nil, err
	}
	files, err := s.store.ListFileMetadataBySnapshot(ctx, active.ID)
	if err != nil {
		return domain.Snapshot{}, nil, err
	}
	base := &gitscan.IncrementalBase{
		ScannerConfigHash: active.ScannerConfigHash, CommitHash: active.CommitHash, TreeHash: active.TreeHash,
		Files: make(map[string]gitscan.PreviousFile, len(files)),
	}
	for _, f := range files {
		base.Files[f.Path] = gitscan.PreviousFile{
			Path: f.Path, BlobOID: f.BlobOID, Mode: f.Mode, Size: f.SizeBytes, SHA256: f.SHA256, LineCount: f.LineCount,
		}
	}
	return active, base, nil
}

func newPublishRequest(projectID, repoID uuid.UUID, jobID *uuid.UUID, scan *gitscan.IncrementalScanResult) repository.PublishSnapshotRequest {
	commit, stats := scan.Commit, scan.Stats
	req := repository.PublishSnapshotRequest{
		ProjectID: projectID,
		Snapshot: domain.Snapshot{
			RepositoryID: repoID, JobID: jobID, ResolvedRef: scan.ResolvedRef, CommitHash: commit.Hash,
			TreeHash: commit.TreeHash, ScannerConfigHash: scan.ScannerConfigHash, ScannedAt: scan.ScannedAt,
			CommitAuthorName: strings.ToValidUTF8(commit.AuthorName, "\uFFFD"), CommitAuthorEmail: strings.ToValidUTF8(commit.AuthorEmail, "\uFFFD"),
			CommitAuthoredAt: commit.AuthoredAt, CommitCommitterName: strings.ToValidUTF8(commit.CommitterName, "\uFFFD"),
			CommitCommitterEmail: strings.ToValidUTF8(commit.CommitterEmail, "\uFFFD"), CommitCommittedAt: commit.CommittedAt,
			CommitSubject: strings.ToValidUTF8(commit.Subject, "\uFFFD"), CommitParents: commit.Parents,
			TotalTreeEntries: stats.TotalTreeEntries, IncludedFiles: stats.IncludedFiles, IncludedBytes: stats.IncludedBytes,
			SkippedFiles: stats.SkippedFiles, SkippedByReason: stats.SkippedByReason, Truncated: stats.Truncated,
			AddedFiles: scan.AddedFiles, ModifiedFiles: scan.ModifiedFiles, DeletedFiles: scan.DeletedFiles,
			UnchangedFiles: scan.UnchangedFiles, BlobsRead: scan.BlobsRead,
		},
	}
	for _, file := range scan.Files {
		if file.Reused {
			req.ReusedFiles = append(req.ReusedFiles, repository.ReusedSnapshotFile{
				SourcePath: file.ReusedFromPath, Path: file.Path, Mode: file.Mode,
			})
			continue
		}
		content := string(file.Content)
		req.NewFiles = append(req.NewFiles, domain.SourceFile{
			Path: file.Path, BlobOID: file.BlobOID, Mode: file.Mode, SizeBytes: file.Size, SHA256: file.SHA256,
			LineCount: file.LineCount, IsExecutable: file.IsExecutable, Content: &content,
		})
	}
	return req
}

func snapshotScope(project, repo string) (uuid.UUID, uuid.UUID, error) {
	projectID, err := parseID(project)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	repoID, err := parseID(repo)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if projectID == uuid.Nil || repoID == uuid.Nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: project_id and repository_id are required", domain.ErrInvalidInput)
	}
	return projectID, repoID, nil
}

func snapshotPage(limit *int, offset int) error {
	if *limit == 0 {
		*limit = 20
	}
	if *limit < 1 || *limit > 100 || offset < 0 {
		return fmt.Errorf("%w: limit must be between 1 and 100 and offset must be nonnegative", domain.ErrInvalidInput)
	}
	return nil
}

func validFileSearchText(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

func (s *SyncService) ListSnapshots(ctx context.Context, project, repo string, filter domain.ListSnapshotsFilter) (domain.SnapshotListResult, error) {
	projectID, repoID, err := snapshotScope(project, repo)
	if err != nil {
		return domain.SnapshotListResult{}, err
	}
	if err := snapshotPage(&filter.Limit, filter.Offset); err != nil {
		return domain.SnapshotListResult{}, err
	}
	return s.store.ListSnapshots(ctx, projectID, repoID, filter)
}

func (s *SyncService) GetActiveSnapshot(ctx context.Context, project, repo string) (domain.Snapshot, error) {
	projectID, repoID, err := snapshotScope(project, repo)
	if err != nil {
		return domain.Snapshot{}, err
	}
	return s.store.GetActiveSnapshotByProject(ctx, projectID, repoID)
}

func (s *SyncService) SearchActiveFiles(ctx context.Context, project, repo string, filter domain.SearchFilesFilter) (domain.SourceFileListResult, error) {
	projectID, repoID, err := snapshotScope(project, repo)
	if err != nil {
		return domain.SourceFileListResult{}, err
	}
	if err := snapshotPage(&filter.Limit, filter.Offset); err != nil {
		return domain.SourceFileListResult{}, err
	}
	if !validFileSearchText(filter.Query) || !validFileSearchText(filter.PathPrefix) {
		return domain.SourceFileListResult{}, fmt.Errorf("%w: invalid q or path_prefix", domain.ErrInvalidInput)
	}
	return s.store.SearchActiveFiles(ctx, projectID, repoID, filter)
}

func (s *SyncService) GetActiveFile(ctx context.Context, project, repo, path string) (domain.SourceFile, error) {
	projectID, repoID, err := snapshotScope(project, repo)
	if err != nil {
		return domain.SourceFile{}, err
	}
	if path == "" || !validFileSearchText(path) || utf8.RuneCountInString(path) > 512 {
		return domain.SourceFile{}, fmt.Errorf("%w: path must contain between 1 and 512 valid characters", domain.ErrInvalidInput)
	}
	return s.store.GetActiveFile(ctx, projectID, repoID, path)
}
