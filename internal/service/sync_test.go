package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/gitscan"
	"projectlens/internal/repository"
)

type syncLookupFunc func(context.Context, uuid.UUID, uuid.UUID) (domain.Repository, error)

func (f syncLookupFunc) GetByID(ctx context.Context, projectID, repoID uuid.UUID) (domain.Repository, error) {
	return f(ctx, projectID, repoID)
}

type incrementalScanFunc func(context.Context, gitscan.SourceSpec, *gitscan.IncrementalBase, gitscan.ScanOptions) (*gitscan.IncrementalScanResult, error)

func (f incrementalScanFunc) ScanIncremental(ctx context.Context, spec gitscan.SourceSpec, base *gitscan.IncrementalBase, options gitscan.ScanOptions) (*gitscan.IncrementalScanResult, error) {
	return f(ctx, spec, base, options)
}

type syncStoreStub struct {
	snapshotStore
	events    []string
	active    domain.Snapshot
	files     []domain.SourceFile
	published domain.Snapshot
	request   repository.PublishSnapshotRequest
	failAt    string
	err       error
	retain    int
	cutoff    time.Time
}

func (s *syncStoreStub) stage(name string) error {
	s.events = append(s.events, name)
	if name == s.failAt {
		return s.err
	}
	return nil
}

func (s *syncStoreStub) AcquireRepoSyncLock(context.Context, uuid.UUID) (func(), error) {
	return func() { s.stage("release") }, s.stage("lock")
}
func (s *syncStoreStub) GetActiveSnapshot(context.Context, uuid.UUID) (domain.Snapshot, error) {
	if err := s.stage("get active"); err != nil {
		return domain.Snapshot{}, err
	}
	if s.active.ID == uuid.Nil {
		return domain.Snapshot{}, domain.ErrNotFound
	}
	return s.active, nil
}
func (s *syncStoreStub) ListFileMetadataBySnapshot(context.Context, uuid.UUID) ([]domain.SourceFile, error) {
	return s.files, s.stage("get files")
}
func (s *syncStoreStub) TouchRepositorySyncedAt(context.Context, uuid.UUID, uuid.UUID) error {
	return s.stage("touch")
}
func (s *syncStoreStub) PublishSnapshot(_ context.Context, _ db.TxManager, req repository.PublishSnapshotRequest) (domain.Snapshot, error) {
	s.request = req
	return s.published, s.stage("publish")
}
func (s *syncStoreStub) PruneSnapshots(_ context.Context, _ uuid.UUID, retain int, cutoff time.Time) (int64, error) {
	s.retain, s.cutoff = retain, cutoff
	if err := s.stage("prune"); err != nil {
		return 0, err
	}
	return 2, nil
}

func TestSyncRepositoryPaths(t *testing.T) {
	projectID, repoID, activeID, publishedID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	active := domain.Snapshot{ID: activeID, Version: 1, CommitHash: "old", TreeHash: "old tree", ScannerConfigHash: "filters"}
	for _, tc := range []struct {
		name, failAt string
		hasActive    bool
		unchanged    bool
		wantErr      bool
		wantEvents   string
	}{
		{name: "first", wantEvents: "lock,lookup,get active,scan,publish,prune,release"},
		{name: "incremental", hasActive: true, wantEvents: "lock,lookup,get active,get files,scan,publish,prune,release"},
		{name: "unchanged", hasActive: true, unchanged: true, wantEvents: "lock,lookup,get active,get files,scan,touch,prune,release"},
		{name: "garbage collection failure is nonfatal", failAt: "prune", wantEvents: "lock,lookup,get active,scan,publish,prune,release"},
		{name: "lock failure", failAt: "lock", wantErr: true, wantEvents: "lock"},
		{name: "lookup failure", failAt: "lookup", wantErr: true, wantEvents: "lock,lookup,release"},
		{name: "active lookup failure", failAt: "get active", wantErr: true, wantEvents: "lock,lookup,get active,release"},
		{name: "file lookup failure", failAt: "get files", hasActive: true, wantErr: true, wantEvents: "lock,lookup,get active,get files,release"},
		{name: "scan failure", failAt: "scan", hasActive: true, wantErr: true, wantEvents: "lock,lookup,get active,get files,scan,release"},
		{name: "publish failure", failAt: "publish", hasActive: true, wantErr: true, wantEvents: "lock,lookup,get active,get files,scan,publish,release"},
		{name: "touch failure", failAt: "touch", hasActive: true, unchanged: true, wantErr: true, wantEvents: "lock,lookup,get active,get files,scan,touch,release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			injected := errors.New("operation failed")
			prior := domain.SourceFile{Path: "kept", BlobOID: "blob", Mode: "100644", SizeBytes: 8, SHA256: "digest", LineCount: 2}
			store := &syncStoreStub{
				failAt: tc.failAt, err: injected, files: []domain.SourceFile{prior},
				published: domain.Snapshot{ID: publishedID, Version: 2, CommitHash: "new", TreeHash: "new tree"},
			}
			if tc.hasActive {
				store.active = active
			}
			lookup := syncLookupFunc(func(_ context.Context, project, repo uuid.UUID) (domain.Repository, error) {
				if project != projectID || repo != repoID {
					t.Fatalf("lookup got project %s and repo %s", project, repo)
				}
				if tc.hasActive {
					return domain.Repository{SourceType: domain.RepositorySourceGitHub, Source: "old-key", RemoteURL: "canonical"}, store.stage("lookup")
				}
				local := "/canonical/local"
				return domain.Repository{SourceType: domain.RepositorySourceLocalGit, Source: "/alias", LocalPath: &local}, store.stage("lookup")
			})
			scanner := incrementalScanFunc(func(_ context.Context, spec gitscan.SourceSpec, base *gitscan.IncrementalBase, options gitscan.ScanOptions) (*gitscan.IncrementalScanResult, error) {
				wantSpec := gitscan.SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: "/canonical/local", Ref: "main"}
				if tc.hasActive {
					wantSpec.SourceType, wantSpec.Source = domain.RepositorySourceGitHub, "canonical"
				}
				if !options.IncludeContent || spec != wantSpec || (base != nil) != tc.hasActive {
					t.Fatalf("scan arguments: %+v, %+v, %+v", spec, options, base)
				}
				if base != nil {
					want := &gitscan.IncrementalBase{
						ScannerConfigHash: active.ScannerConfigHash, CommitHash: active.CommitHash, TreeHash: active.TreeHash,
						Files: map[string]gitscan.PreviousFile{prior.Path: {
							Path: prior.Path, BlobOID: prior.BlobOID, Size: prior.SizeBytes, Mode: prior.Mode, SHA256: prior.SHA256, LineCount: prior.LineCount,
						}},
					}
					if !reflect.DeepEqual(base, want) {
						t.Fatalf("base = %+v, want %+v", base, want)
					}
				}
				if err := store.stage("scan"); err != nil {
					return nil, err
				}
				if tc.unchanged {
					return &gitscan.IncrementalScanResult{Unchanged: true, UnchangedFiles: 1}, nil
				}
				scan := &gitscan.IncrementalScanResult{
					ResolvedRef: "main", ScannerConfigHash: "new filters", BlobsRead: 1, AddedFiles: 1,
					Commit: gitscan.CommitInfo{Hash: "new", TreeHash: "new tree", Subject: "changes"},
					Files:  []gitscan.IncrementalFile{{FileEntry: gitscan.FileEntry{Path: "new", Content: []byte("text")}}},
				}
				if tc.hasActive {
					scan.Files = append(scan.Files, gitscan.IncrementalFile{
						FileEntry: gitscan.FileEntry{Path: "renamed", Mode: "100755"}, Reused: true, ReusedFromPath: prior.Path,
					})
				}
				return scan, nil
			})
			svc := NewSyncService(lookup, store, nil, scanner, config.SyncConfig{RetainSuperseded: 3, FailedRetention: time.Hour}, nil)
			earliest := time.Now().Add(-time.Hour)
			result, err := svc.SyncRepository(t.Context(), projectID, repoID, "main", &jobID)
			if got := strings.Join(store.events, ","); got != tc.wantEvents {
				t.Fatalf("calls = %s, want %s", got, tc.wantEvents)
			}
			if tc.wantErr {
				if !errors.Is(err, injected) || result != (domain.SyncResult{}) {
					t.Fatalf("failed sync: %+v, %v", result, err)
				}
				return
			}
			if err != nil || result.RepositoryID != repoID || store.retain != 3 ||
				store.cutoff.Before(earliest) || store.cutoff.After(time.Now().Add(-time.Hour)) {
				t.Fatalf("sync result = %+v, error = %v, retention = %d/%s", result, err, store.retain, store.cutoff)
			}
			if tc.failAt == "prune" && result.PrunedSnapshots != 0 || tc.failAt == "" && result.PrunedSnapshots != 2 {
				t.Fatalf("pruned snapshots: %+v", result)
			}
			if tc.unchanged {
				if result.Status != domain.SyncOutcomeUnchanged || result.SnapshotID != activeID || result.Version != 1 || result.BlobsRead != 0 || result.UnchangedFiles != 1 {
					t.Fatalf("unchanged result: %+v", result)
				}
				return
			}
			if result.Status != domain.SyncOutcomePublished || result.SnapshotID != publishedID || result.Version != 2 || result.BlobsRead != 1 {
				t.Fatalf("published result: %+v", result)
			}
			req := store.request
			if req.ProjectID != projectID || req.Snapshot.RepositoryID != repoID || req.Snapshot.JobID == nil || *req.Snapshot.JobID != jobID ||
				req.Snapshot.CommitSubject != "changes" || req.Snapshot.ScannerConfigHash != "new filters" || req.Snapshot.BlobsRead != 1 ||
				len(req.NewFiles) != 1 || req.NewFiles[0].Content == nil || *req.NewFiles[0].Content != "text" {
				t.Fatalf("publish request: %+v", req)
			}
			if tc.hasActive {
				want := repository.ReusedSnapshotFile{SourcePath: "kept", Path: "renamed", Mode: "100755"}
				if req.ExpectedParentSnapshotID == nil || *req.ExpectedParentSnapshotID != activeID || len(req.ReusedFiles) != 1 || req.ReusedFiles[0] != want {
					t.Fatalf("incremental publish request: %+v", req)
				}
			} else if req.ExpectedParentSnapshotID != nil || len(req.ReusedFiles) != 0 {
				t.Fatalf("first publish request: %+v", req)
			}
		})
	}
}

type readSnapshotStore struct {
	snapshotStore
	projectID, repoID uuid.UUID
}

func (s readSnapshotStore) SearchActiveFiles(_ context.Context, projectID, repoID uuid.UUID, filter domain.SearchFilesFilter) (domain.SourceFileListResult, error) {
	if projectID != s.projectID || repoID != s.repoID {
		return domain.SourceFileListResult{}, domain.ErrNotFound
	}
	return domain.SourceFileListResult{Limit: filter.Limit, Offset: filter.Offset, Items: []domain.SourceFile{{Path: filter.Query + filter.PathPrefix}}}, nil
}

func TestSyncReadValidation(t *testing.T) {
	project, repo := uuid.New(), uuid.New()
	svc := NewSyncService(nil, readSnapshotStore{projectID: project, repoID: repo}, nil, nil, config.SyncConfig{}, nil)
	if _, err := svc.SyncRepository(t.Context(), uuid.Nil, repo, "", nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("nil project in sync: %v", err)
	}
	if _, err := svc.ListSnapshots(t.Context(), project.String(), repo.String(), domain.ListSnapshotsFilter{Limit: 101}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid snapshot page: %v", err)
	}
	if _, err := svc.GetActiveSnapshot(t.Context(), project.String(), uuid.Nil.String()); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("nil repository ID: %v", err)
	}
	if _, err := svc.GetActiveFile(t.Context(), project.String(), repo.String(), ""); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("missing path: %v", err)
	}
	for _, filter := range []domain.SearchFilesFilter{{Offset: -1}, {Query: "invalid\x00search"}, {PathPrefix: "\xff"}} {
		if _, err := svc.SearchActiveFiles(t.Context(), project.String(), repo.String(), filter); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("invalid search filter %+v: %v", filter, err)
		}
	}
	got, err := svc.SearchActiveFiles(t.Context(), project.String(), repo.String(), domain.SearchFilesFilter{Query: "x", PathPrefix: "y", Offset: 3})
	if err != nil || got.Limit != 20 || got.Offset != 3 || len(got.Items) != 1 || got.Items[0].Path != "xy" {
		t.Fatalf("search page: %+v, %v", got, err)
	}
	if _, err := svc.SearchActiveFiles(t.Context(), uuid.NewString(), repo.String(), domain.SearchFilesFilter{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("incorrect project scope: %v", err)
	}
}
