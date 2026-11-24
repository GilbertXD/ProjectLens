package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/github"
	"projectlens/internal/gitscan"
	"projectlens/internal/handler"
	"projectlens/internal/repository"
	"projectlens/internal/router"
	"projectlens/internal/service"
	"projectlens/internal/worker"
)

type syncFixture struct {
	cfg       config.Config
	pool      *pgxpool.Pool
	store     *repository.PostgresSnapshotStore
	repos     *repository.PostgresRepositoryStore
	tx        *db.PgxTxManager
	log       *slog.Logger
	scanner   *gitscan.Scanner
	svc       *service.SyncService
	projectID uuid.UUID
	repoID    uuid.UUID
	dir       string
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	cfg := database(t)
	cfg.Scanner = config.DefaultScannerConfig()
	cfg.Scanner.CacheDir = t.TempDir()
	cfg.Sync = config.SyncConfig{RetainSuperseded: 10, FailedRetention: 24 * time.Hour}
	cfg.GitHub.Token = os.Getenv("GITHUB_TOKEN")
	cfg.Worker = config.WorkerConfig{Concurrency: 1, PollInterval: 10 * time.Millisecond, LeaseDuration: time.Second,
		HeartbeatInterval: 100 * time.Millisecond, DefaultTimeout: 15 * time.Second, DefaultMaxAttempts: 2,
		RetryBaseDelay: 10 * time.Millisecond, RetryMaxDelay: time.Second, ShutdownTimeout: time.Second}
	pool := openPool(t, cfg)
	t.Cleanup(pool.Close)
	if err := db.MigrateUp(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	store, err := repository.NewPostgresSnapshotStore(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	tx := db.NewPgxTxManager(pool)
	projects := service.NewProjectService(repository.NewPostgresProjectRepository(pool), tx)
	project, err := projects.Create(t.Context(), domain.CreateProjectInput{Slug: "sync-test", Name: "Sync Test"})
	if err != nil {
		t.Fatal(err)
	}
	return &syncFixture{
		cfg: cfg, pool: pool, store: store, repos: repository.NewPostgresRepositoryStore(pool), tx: tx,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), projectID: project.ID,
	}
}

func (f *syncFixture) attach(t *testing.T, repo domain.Repository) {
	t.Helper()
	repo.ProjectID, repo.ID, repo.Name = f.projectID, uuid.New(), "Sync Fixture"
	repo.CanonicalSource = repo.Source
	if repo.LocalPath != nil {
		f.cfg.Repository.AllowedLocalRoots = []string{*repo.LocalPath}
	}
	created, err := f.repos.Create(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	f.repoID = created.ID
	f.scanner = gitscan.NewScanner(f.cfg.Scanner, f.cfg.Repository, f.cfg.GitHub)
	f.svc = service.NewSyncService(f.repos, f.store, f.tx, f.scanner, f.cfg.Sync, f.log)
}

func localSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	f := newSyncFixture(t)
	f.dir = t.TempDir()
	syncGit(t, f.dir, "init", "--initial-branch=main", "--template=")
	f.attach(t, domain.Repository{SourceType: domain.RepositorySourceLocalGit, Source: f.dir, LocalPath: &f.dir})
	return f
}

func syncGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Sync Test", "GIT_AUTHOR_EMAIL=sync@example.org",
		"GIT_COMMITTER_NAME=Sync Test", "GIT_COMMITTER_EMAIL=sync@example.org",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *syncFixture) write(t *testing.T, path, content string) {
	t.Helper()
	path = filepath.Join(f.dir, path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *syncFixture) commit(t *testing.T, subject string) string {
	t.Helper()
	syncGit(t, f.dir, "add", "-A")
	return f.commitIndex(t, subject)
}

func (f *syncFixture) commitIndex(t *testing.T, subject string) string {
	t.Helper()
	syncGit(t, f.dir, "commit", "--no-gpg-sign", "-m", subject)
	return syncGit(t, f.dir, "rev-parse", "HEAD")
}

func (f *syncFixture) sync(t *testing.T, ref string) domain.SyncResult {
	t.Helper()
	result, err := f.svc.SyncRepository(t.Context(), f.projectID, f.repoID, ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *syncFixture) active(t *testing.T) domain.Snapshot {
	t.Helper()
	active, err := f.store.GetActiveSnapshotByProject(t.Context(), f.projectID, f.repoID)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func (f *syncFixture) search(t *testing.T, filter domain.SearchFilesFilter) domain.SourceFileListResult {
	t.Helper()
	files, err := f.svc.SearchActiveFiles(t.Context(), f.projectID.String(), f.repoID.String(), filter)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func (f *syncFixture) file(t *testing.T, path, content string, executable bool) domain.SourceFile {
	t.Helper()
	file, err := f.store.GetActiveFile(t.Context(), f.projectID, f.repoID, path)
	if err != nil || file.Content == nil || *file.Content != content || file.IsExecutable != executable ||
		file.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(content))) || file.SizeBytes != int64(len(content)) {
		t.Fatalf("active file %q = %+v, %v", path, file, err)
	}
	return file
}

func (f *syncFixture) repo(t *testing.T) domain.Repository {
	t.Helper()
	repo, err := f.repos.GetByID(t.Context(), f.projectID, f.repoID)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func (f *syncFixture) startHTTP(t *testing.T, jobs *handler.JobHandler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(router.New(f.cfg, f.log, router.Dependencies{
		Snapshots: handler.NewSnapshotHandler(f.svc, f.log), Jobs: jobs, Pinger: f.pool,
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *syncFixture) apiPath() string {
	return projects + "/" + f.projectID.String() + "/repositories/" + f.repoID.String()
}

func TestSnapshotSyncIncremental(t *testing.T) {
	f := localSyncFixture(t)
	empty := f.search(t, domain.SearchFilesFilter{})
	if empty.Total != 0 || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("search before first sync: %+v", empty)
	}
	if _, err := f.store.SearchActiveFiles(t.Context(), uuid.New(), f.repoID, domain.SearchFilesFilter{Limit: 10}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("incorrect project scope: %v", err)
	}
	for path, content := range map[string]string{
		"src/change.txt": "old content\n", "src/delete.txt": "gone uniquely\n", "src/old.txt": "reused file\n",
		"src/run.sh": "#!/bin/sh\nexit 0\n", "keep.txt": "literal 10%_ and \\backslash\n", "empty.txt": "",
	} {
		f.write(t, path, content)
	}
	firstCommit := f.commit(t, "initial files")
	first := f.sync(t, "")
	active := f.active(t)
	if first.Status != domain.SyncOutcomePublished || first.Version != 1 || first.BlobsRead != 6 || first.AddedFiles != 6 ||
		active.ID != first.SnapshotID || active.Status != domain.SnapshotStatusActive || active.ParentSnapshotID != nil ||
		active.PublishedAt == nil || active.SupersededAt != nil || active.CommitHash != firstCommit ||
		active.CommitAuthorName != "Sync Test" || active.CommitSubject != "initial files" ||
		len(active.CommitParents) != 0 || active.IncludedFiles != 6 || active.IncludedBytes == 0 {
		t.Fatalf("initial result = %+v, active = %+v", first, active)
	}
	if f.file(t, "src/run.sh", "#!/bin/sh\nexit 0\n", false).LineCount != 2 || f.file(t, "empty.txt", "", false).LineCount != 0 {
		t.Fatal("incorrect line counts")
	}
	metadata, err := f.store.ListFileMetadataBySnapshot(t.Context(), active.ID)
	if err != nil || len(metadata) != 6 {
		t.Fatalf("snapshot file metadata: %+v, %v", metadata, err)
	}
	for _, file := range metadata {
		if file.Content != nil || file.SnapshotID != active.ID || file.BlobOID != syncGit(t, f.dir, "rev-parse", "HEAD:"+file.Path) {
			t.Fatalf("incorrect file metadata: %+v", file)
		}
	}
	firstSynced := f.repo(t).LastSyncedAt
	if firstSynced == nil {
		t.Fatal("first sync did not set last_synced_at")
	}
	unchanged := f.sync(t, "main")
	if unchanged.Status != domain.SyncOutcomeUnchanged || unchanged.SnapshotID != active.ID || unchanged.Version != 1 ||
		unchanged.BlobsRead != 0 || unchanged.UnchangedFiles != 6 {
		t.Fatalf("unchanged result: %+v", unchanged)
	}
	if last := f.repo(t).LastSyncedAt; last == nil || !last.After(*firstSynced) {
		t.Fatalf("unchanged sync did not refresh last_synced_at: %v", last)
	}
	list, err := f.svc.ListSnapshots(t.Context(), f.projectID.String(), f.repoID.String(), domain.ListSnapshotsFilter{})
	if err != nil || list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("unchanged snapshot list: %+v, %v", list, err)
	}

	syncGit(t, f.dir, "mv", "src/old.txt", "src/renamed.txt")
	syncGit(t, f.dir, "rm", "src/delete.txt")
	f.write(t, "src/change.txt", "updated content\n")
	f.write(t, "src/added.txt", "brand new\n")
	syncGit(t, f.dir, "add", "-A")
	syncGit(t, f.dir, "update-index", "--chmod=+x", "src/run.sh")
	secondCommit := f.commitIndex(t, "incremental changes")
	second := f.sync(t, "")
	next := f.active(t)
	if second.Status != domain.SyncOutcomePublished || second.Version != 2 || second.CommitHash != secondCommit ||
		second.BlobsRead != 2 || second.AddedFiles != 2 || second.ModifiedFiles != 2 || second.DeletedFiles != 2 ||
		second.UnchangedFiles != 2 || next.ParentSnapshotID == nil || *next.ParentSnapshotID != first.SnapshotID ||
		!reflect.DeepEqual(next.CommitParents, []string{firstCommit}) {
		t.Fatalf("incremental result: %+v, active = %+v", second, next)
	}
	f.file(t, "src/change.txt", "updated content\n", false)
	f.file(t, "src/renamed.txt", "reused file\n", false)
	if f.file(t, "src/run.sh", "#!/bin/sh\nexit 0\n", true).Mode != "100755" {
		t.Fatal("executable mode was not copied")
	}
	for _, path := range []string{"src/delete.txt", "src/old.txt"} {
		if _, err := f.store.GetActiveFile(t.Context(), f.projectID, f.repoID, path); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("deleted %s: %v", path, err)
		}
	}
	for _, query := range []string{"gone uniquely", "old content"} {
		if got := f.search(t, domain.SearchFilesFilter{Query: query}); got.Total != 0 {
			t.Fatalf("search included superseded content: %+v", got)
		}
	}
	for _, query := range []string{"%_", "\\BACKSLASH", "LiTeRaL"} {
		if got := f.search(t, domain.SearchFilesFilter{Query: query}); got.Total != 1 || len(got.Items) != 1 ||
			got.Items[0].Path != "keep.txt" || got.Items[0].Content != nil {
			t.Fatalf("literal search for %q: %+v", query, got)
		}
	}
	if got := f.search(t, domain.SearchFilesFilter{PathPrefix: "src/", Limit: 1, Offset: 1}); got.Total != 4 ||
		len(got.Items) != 1 || got.Items[0].Path != "src/change.txt" {
		t.Fatalf("ordered page: %+v", got)
	}
	if got := f.search(t, domain.SearchFilesFilter{PathPrefix: "src/", Limit: 1, Offset: 9}); got.Total != 4 ||
		got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("past the last page: %+v", got)
	}

	f.cfg.Scanner.ExcludePatterns = []string{"added.txt"}
	changedScanner := gitscan.NewScanner(f.cfg.Scanner, f.cfg.Repository, f.cfg.GitHub)
	f.svc = service.NewSyncService(f.repos, f.store, f.tx, changedScanner, f.cfg.Sync, f.log)
	third := f.sync(t, "")
	if third.Status != domain.SyncOutcomePublished || third.Version != 3 || third.CommitHash != second.CommitHash ||
		third.BlobsRead != 0 || third.DeletedFiles != 1 || f.active(t).ScannerConfigHash == next.ScannerConfigHash {
		t.Fatalf("config-only rescan: %+v", third)
	}
	list, err = f.svc.ListSnapshots(t.Context(), f.projectID.String(), f.repoID.String(), domain.ListSnapshotsFilter{Limit: 2, Offset: 1})
	if err != nil || list.Total != 3 || len(list.Items) != 2 ||
		list.Items[0].Version != 2 || list.Items[0].Status != domain.SnapshotStatusSuperseded ||
		list.Items[0].SupersededAt == nil || list.Items[1].Version != 1 {
		t.Fatalf("snapshot history: %+v, %v", list, err)
	}
}

type publishHookStore struct {
	*repository.PostgresSnapshotStore
	hook func(context.Context, uuid.UUID) error
}

func (s publishHookStore) PublishSnapshot(ctx context.Context, tx db.TxManager, req repository.PublishSnapshotRequest) (domain.Snapshot, error) {
	req.BeforePromoteHook = s.hook
	return s.PostgresSnapshotStore.PublishSnapshot(ctx, tx, req)
}

type syncAttempt struct {
	result domain.SyncResult
	err    error
}

func (f *syncFixture) syncInBackground(ctx context.Context, svc *service.SyncService, ref string) <-chan syncAttempt {
	done := make(chan syncAttempt, 1)
	go func() {
		defer close(done)
		result, err := svc.SyncRepository(ctx, f.projectID, f.repoID, ref, nil)
		done <- syncAttempt{result, err}
	}()
	return done
}

func waitSyncAttempt(t *testing.T, done <-chan syncAttempt) syncAttempt {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for sync")
		return syncAttempt{}
	}
}

func (f *syncFixture) interruptScan(t *testing.T) {
	t.Helper()
	shim := t.TempDir()
	marker := filepath.Join(shim, "batch-started")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
for arg do
	if [ "$arg" = "--batch" ]; then
		touch "$PROJECTLENS_TEST_BATCH_MARKER"
		exec sleep 30
	fi
done
exec "$PROJECTLENS_TEST_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJECTLENS_TEST_GIT", git)
	t.Setenv("PROJECTLENS_TEST_BATCH_MARKER", marker)
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := f.syncInBackground(ctx, f.svc, "")
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("sync exited before reaching the Git blob reader: %+v", result)
		case <-ctx.Done():
			t.Fatal("Git blob reader did not start")
		case <-ticker.C:
		}
	}
	cancel()
	if result := waitSyncAttempt(t, done); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("interrupted scan: %+v", result)
	}
}

func TestSnapshotSyncRollbackAndMVCC(t *testing.T) {
	f := localSyncFixture(t)
	f.write(t, "data.txt", "old-reader-token\n")
	f.write(t, "kept.txt", "copied through promotion\n")
	f.commit(t, "first")
	first := f.sync(t, "")
	firstSynced := f.repo(t).LastSyncedAt
	f.write(t, "data.txt", "new-reader-token\n")
	f.commit(t, "second")
	srv := f.startHTTP(t, nil)

	assertOld := func(t *testing.T) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		active, err := f.store.GetActiveSnapshotByProject(ctx, f.projectID, f.repoID)
		if err != nil || active.ID != first.SnapshotID || active.Status != domain.SnapshotStatusActive {
			t.Fatalf("old active snapshot = %+v, error = %v", active, err)
		}
		page, err := f.store.SearchActiveFiles(ctx, f.projectID, f.repoID, domain.SearchFilesFilter{Query: "old-reader-token", Limit: 10})
		if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].SnapshotID != first.SnapshotID {
			t.Fatalf("search of old snapshot: %+v, %v", page, err)
		}
		page = domain.SourceFileListResult{}
		request(t, srv, http.MethodGet, f.apiPath()+"/files?q=old-reader-token", "", http.StatusOK, &page)
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].SnapshotID != first.SnapshotID {
			t.Fatalf("HTTP search of old snapshot: %+v", page)
		}
		page = domain.SourceFileListResult{}
		request(t, srv, http.MethodGet, f.apiPath()+"/files?q=new-reader-token", "", http.StatusOK, &page)
		if page.Total != 0 || len(page.Items) != 0 {
			t.Fatalf("unpublished contents visible over HTTP: %+v", page)
		}
	}

	t.Run("canceled during Git blob read", func(t *testing.T) {
		f.interruptScan(t)
		assertOld(t)
		assertSnapshotCounts(t, f, 1, 2)
	})
	var stagedID uuid.UUID
	injected := errors.New("failure before promote")
	failureStore := publishHookStore{f.store, func(_ context.Context, id uuid.UUID) error {
		stagedID = id
		return injected
	}}
	failing := service.NewSyncService(f.repos, failureStore, f.tx, f.scanner, f.cfg.Sync, f.log)
	if _, err := failing.SyncRepository(t.Context(), f.projectID, f.repoID, "", nil); !errors.Is(err, injected) || stagedID == uuid.Nil {
		t.Fatalf("publish failure: staged ID = %v, error = %v", stagedID, err)
	}
	assertOld(t)
	assertSnapshotCounts(t, f, 1, 2)
	if last := f.repo(t).LastSyncedAt; firstSynced == nil || last == nil || !last.Equal(*firstSynced) {
		t.Fatalf("failed sync changed last_synced_at from %v to %v", firstSynced, last)
	}

	started := make(chan uuid.UUID, 1)
	promote := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pausedStore := publishHookStore{f.store, func(ctx context.Context, id uuid.UUID) error {
		var status string
		var files int
		conn := db.ConnFromContext(ctx, f.pool)
		if err := conn.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM source_files WHERE snapshot_id = $1)
 FROM snapshots WHERE id = $1`, id).Scan(&status, &files); err != nil {
			return err
		}
		if status != "staging" || files != 2 {
			return fmt.Errorf("publish hook observed status %q and %d files", status, files)
		}
		started <- id
		select {
		case <-promote:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	retry := service.NewSyncService(f.repos, pausedStore, f.tx, f.scanner, f.cfg.Sync, f.log)
	done := f.syncInBackground(ctx, retry, "")
	t.Cleanup(func() { cancel(); <-done })
	var nextID uuid.UUID
	select {
	case nextID = <-started:
	case result := <-done:
		t.Fatalf("publish ended early: %+v", result)
	case <-ctx.Done():
		t.Fatal("publish did not reach promotion hook")
	}
	assertOld(t)
	assertSnapshotCounts(t, f, 1, 2)
	close(promote)
	attempt := waitSyncAttempt(t, done)
	if attempt.err != nil || attempt.result.SnapshotID != nextID || attempt.result.Version != 2 ||
		attempt.result.Status != domain.SyncOutcomePublished || attempt.result.BlobsRead != 1 || nextID == stagedID {
		t.Fatalf("retry result: %+v", attempt)
	}
	if active := f.active(t); active.ID != nextID || active.Status != domain.SnapshotStatusActive {
		t.Fatalf("new active snapshot: %+v", active)
	}
	assertSnapshotCounts(t, f, 2, 4)
	var page domain.SourceFileListResult
	request(t, srv, http.MethodGet, f.apiPath()+"/files?q=new-reader-token", "", http.StatusOK, &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].SnapshotID != nextID {
		t.Fatalf("HTTP search after promotion: %+v", page)
	}
	request(t, srv, http.MethodGet, f.apiPath()+"/files?q=old-reader-token", "", http.StatusOK, &page)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("old content remained visible: %+v", page)
	}
}

func assertSnapshotCounts(t *testing.T, f *syncFixture, snapshots, files int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var gotSnapshots, gotFiles int
	err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM snapshots WHERE repository_id = $1),
 (SELECT count(*) FROM source_files WHERE repository_id = $1)`, f.repoID).Scan(&gotSnapshots, &gotFiles)
	if err != nil || gotSnapshots != snapshots || gotFiles != files {
		t.Fatalf("found %d snapshots and %d files, want %d snapshots and %d files: %v", gotSnapshots, gotFiles, snapshots, files, err)
	}
}

func (f *syncFixture) raceSyncs(t *testing.T, refs []string) []domain.SyncResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	results := make([]syncAttempt, len(refs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Go(func() {
			<-start
			results[i].result, results[i].err = f.svc.SyncRepository(ctx, f.projectID, f.repoID, ref, nil)
		})
	}
	close(start)
	wg.Wait()
	out := make([]domain.SyncResult, len(results))
	for i, attempt := range results {
		if attempt.err != nil {
			t.Fatalf("concurrent sync for %q: %v", refs[i], attempt.err)
		}
		out[i] = attempt.result
	}
	return out
}

func TestSnapshotSyncConcurrency(t *testing.T) {
	f := localSyncFixture(t)
	f.write(t, "data", "one")
	f.commit(t, "one")
	const count = 8
	results := f.raceSyncs(t, make([]string, count))
	first := f.active(t)
	var published int
	for _, result := range results {
		if result.SnapshotID != first.ID || result.Version != 1 {
			t.Fatalf("duplicate first snapshot: %+v", result)
		}
		switch {
		case result.Status == domain.SyncOutcomePublished && result.BlobsRead == 1:
			published++
		case result.Status == domain.SyncOutcomeUnchanged && result.BlobsRead == 0:
		default:
			t.Fatalf("invalid concurrent sync result: %+v", result)
		}
	}
	if published != 1 {
		t.Fatalf("first snapshot published %d times", published)
	}
	assertSnapshotCounts(t, f, 1, 1)

	refs := make([]string, 4)
	for i := range refs {
		content := fmt.Sprintf("commit %d", i+2)
		f.write(t, "data", content)
		refs[i] = f.commit(t, content)
	}
	results = f.raceSyncs(t, refs)
	versions := make([]int64, len(refs))
	for i, result := range results {
		if result.Status != domain.SyncOutcomePublished || result.CommitHash != refs[i] || result.BlobsRead != 1 {
			t.Fatalf("concurrent distinct commit %s: %+v", refs[i], result)
		}
		versions[i] = result.Version
	}
	slices.Sort(versions)
	if !slices.Equal(versions, []int64{2, 3, 4, 5}) {
		t.Fatalf("versions: %v", versions)
	}
	list, err := f.svc.ListSnapshots(t.Context(), f.projectID.String(), f.repoID.String(), domain.ListSnapshotsFilter{})
	if err != nil || list.Total != 5 || len(list.Items) != 5 {
		t.Fatalf("snapshot history: %+v, %v", list, err)
	}
	active := list.Items[0]
	if active.Version != 5 || active.Status != domain.SnapshotStatusActive {
		t.Fatalf("active snapshot: %+v", active)
	}
	for i, item := range list.Items[1:] {
		child := list.Items[i]
		if item.Status != domain.SnapshotStatusSuperseded || child.ParentSnapshotID == nil || *child.ParentSnapshotID != item.ID {
			t.Fatalf("snapshot versions out of order: %+v", list.Items)
		}
	}
	if _, err := f.store.PublishSnapshot(t.Context(), f.tx, repository.PublishSnapshotRequest{
		ProjectID: f.projectID, Snapshot: active, ExpectedParentSnapshotID: &first.ID,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale expected parent returned %v", err)
	}
	if _, err := f.store.PublishSnapshot(t.Context(), f.tx, repository.PublishSnapshotRequest{
		ProjectID: f.projectID, Snapshot: active, ExpectedParentSnapshotID: &active.ID,
		ReusedFiles: []repository.ReusedSnapshotFile{{SourcePath: "missing", Path: "target", Mode: "100644"}},
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("missing source path was accepted: %v", err)
	}
	if got := f.active(t); got.ID != active.ID {
		t.Fatalf("failed publish changed active snapshot: %+v", got)
	}
	assertSnapshotCounts(t, f, 5, 5)

	release, err := f.store.AcquireRepoSyncLock(t.Context(), f.repoID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	waitCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if unlock, err := f.store.AcquireRepoSyncLock(waitCtx, f.repoID); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("lock waiter did not honor context: %v", err)
	}
}

func TestSnapshotSyncRetention(t *testing.T) {
	f := localSyncFixture(t)
	f.cfg.Sync.RetainSuperseded = 2
	f.svc = service.NewSyncService(f.repos, f.store, f.tx, f.scanner, f.cfg.Sync, f.log)
	history := make([]domain.SyncResult, 5)
	for i := range history {
		content := fmt.Sprintf("version %d", i+1)
		f.write(t, "history.txt", content)
		f.commit(t, content)
		history[i] = f.sync(t, "")
		var wantPruned int64
		if i >= 3 {
			wantPruned = 1
		}
		if history[i].Version != int64(i+1) || history[i].PrunedSnapshots != wantPruned {
			t.Fatalf("retention at version %d: %+v", i+1, history[i])
		}
	}
	assertSnapshotCounts(t, f, 3, 3)
	list, err := f.svc.ListSnapshots(t.Context(), f.projectID.String(), f.repoID.String(), domain.ListSnapshotsFilter{})
	if err != nil || list.Total != 3 || len(list.Items) != 3 {
		t.Fatalf("retained snapshots: %+v, %v", list, err)
	}
	for i, item := range list.Items {
		if item.ID != history[4-i].SnapshotID || (i == 0 && item.Status != domain.SnapshotStatusActive) ||
			(i > 0 && item.Status != domain.SnapshotStatusSuperseded) {
			t.Fatalf("incorrect retained versions: %+v", list.Items)
		}
	}
	for _, pruned := range history[:2] {
		files, err := f.store.ListFileMetadataBySnapshot(t.Context(), pruned.SnapshotID)
		if err != nil || len(files) != 0 {
			t.Fatalf("pruned source files: %+v, %v", files, err)
		}
	}

	var staleIDs, freshIDs []uuid.UUID
	for i, status := range []domain.SnapshotStatus{
		domain.SnapshotStatusFailed, domain.SnapshotStatusStaging, domain.SnapshotStatusFailed, domain.SnapshotStatusStaging,
	} {
		id, created := uuid.New(), time.Now().UTC()
		if i < 2 {
			created = created.Add(-48 * time.Hour)
			staleIDs = append(staleIDs, id)
		} else {
			freshIDs = append(freshIDs, id)
		}
		_, err := f.pool.Exec(t.Context(), `INSERT INTO snapshots (
 id, repository_id, version, status, resolved_ref, commit_hash, tree_hash, scanner_config_hash,
 commit_author_name, commit_author_email, commit_authored_at, commit_committer_name, commit_committer_email,
 commit_committed_at, commit_subject, total_tree_entries, included_files, included_bytes, skipped_files,
 added_files, modified_files, deleted_files, unchanged_files, blobs_read, scanned_at, created_at)
 SELECT $1, repository_id, $2, $3, resolved_ref, commit_hash, tree_hash, scanner_config_hash,
 commit_author_name, commit_author_email, commit_authored_at, commit_committer_name, commit_committer_email,
 commit_committed_at, commit_subject, 1, 1, included_bytes, 0, 1, 0, 0, 0, 1, scanned_at, $4
 FROM snapshots WHERE id = $5`, id, int64(6+i), status, created, history[4].SnapshotID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.pool.Exec(t.Context(), `INSERT INTO source_files
 (id, snapshot_id, repository_id, path, blob_oid, mode, size_bytes, sha256, line_count, is_executable, content)
 SELECT $1, $2, repository_id, path, blob_oid, mode, size_bytes, sha256, line_count, is_executable, content
 FROM source_files WHERE snapshot_id = $3`, uuid.New(), id, history[4].SnapshotID)
		if err != nil {
			t.Fatal(err)
		}
	}
	assertSnapshotCounts(t, f, 7, 7)

	f.cfg.Sync.RetainSuperseded = 0
	f.svc = service.NewSyncService(f.repos, f.store, f.tx, f.scanner, f.cfg.Sync, f.log)
	result := f.sync(t, "")
	if result.Status != domain.SyncOutcomeUnchanged || result.Version != 5 ||
		result.SnapshotID != history[4].SnapshotID || result.PrunedSnapshots != 4 {
		t.Fatalf("zero superseded retention: %+v", result)
	}
	assertSnapshotCounts(t, f, 3, 3)
	if active := f.active(t); active.ID != history[4].SnapshotID || active.ParentSnapshotID != nil {
		t.Fatalf("active snapshot after pruning its parent: %+v", active)
	}
	f.file(t, "history.txt", "version 5", false)
	for _, id := range append([]uuid.UUID{history[2].SnapshotID, history[3].SnapshotID}, staleIDs...) {
		files, err := f.store.ListFileMetadataBySnapshot(t.Context(), id)
		if err != nil || len(files) != 0 {
			t.Fatalf("pruned snapshot still owns files: %+v, %v", files, err)
		}
	}
	for _, id := range freshIDs {
		files, err := f.store.ListFileMetadataBySnapshot(t.Context(), id)
		if err != nil || len(files) != 1 {
			t.Fatalf("fresh failed/staging snapshot lost files: %+v, %v", files, err)
		}
	}
}

func TestSnapshotSyncJob(t *testing.T) {
	f := localSyncFixture(t)
	f.write(t, "src/job.txt", "content from worker\n")
	f.commit(t, "worker snapshot")
	registry := &worker.Registry{}
	if err := worker.RegisterSyncHandler(registry, f.svc); err != nil {
		t.Fatal(err)
	}
	jobs := repository.NewPostgresJobStore(f.pool)
	jobService := service.NewJobService(jobs, registry, f.cfg.Worker)
	srv := f.startHTTP(t, handler.NewJobHandler(jobService, f.log))

	const path = "/api/v1/jobs"
	payload := fmt.Sprintf(`{"type":"repository.sync","payload":{"project_id":%q,"repository_id":%q,"ref":"main"}}`, f.projectID, f.repoID)
	var queued domain.Job
	request(t, srv, http.MethodPost, path, payload, http.StatusAccepted, &queued)
	if queued.Status != domain.JobPending || queued.FinishedAt != nil {
		t.Fatalf("job was not queued asynchronously: %+v", queued)
	}
	startJobWorker(t, f.cfg.Worker, jobs, registry)
	waitJob(t, jobs, queued.ID, func(job domain.Job) bool {
		return job.Status == domain.JobSucceeded || job.Status == domain.JobFailed
	})
	var finished domain.Job
	request(t, srv, http.MethodGet, path+"/"+queued.ID.String(), "", http.StatusOK, &finished)
	if finished.Status != domain.JobSucceeded {
		t.Fatalf("job did not succeed: %+v", finished)
	}
	var result domain.SyncResult
	if err := json.Unmarshal(finished.Result, &result); err != nil || result.Status != domain.SyncOutcomePublished ||
		result.SnapshotID == uuid.Nil || result.Version != 1 || result.BlobsRead != 1 {
		t.Fatalf("job sync result: %+v, %v", result, err)
	}
	base := f.apiPath()
	var active domain.Snapshot
	request(t, srv, http.MethodGet, base+"/snapshots/active", "", http.StatusOK, &active)
	if active.ID != result.SnapshotID || active.JobID == nil || *active.JobID != queued.ID {
		t.Fatalf("job snapshot: %+v", active)
	}
	var history domain.SnapshotListResult
	request(t, srv, http.MethodGet, base+"/snapshots?limit=1", "", http.StatusOK, &history)
	if history.Total != 1 || len(history.Items) != 1 || history.Items[0].ID != active.ID {
		t.Fatalf("job snapshot history: %+v", history)
	}
	var files domain.SourceFileListResult
	request(t, srv, http.MethodGet, base+"/files?q=WoRkEr&path_prefix=src%2F", "", http.StatusOK, &files)
	if files.Total != 1 || len(files.Items) != 1 || files.Items[0].Path != "src/job.txt" ||
		files.Items[0].Content != nil || files.Items[0].SnapshotID != active.ID {
		t.Fatalf("job snapshot search: %+v", files)
	}
	var file domain.SourceFile
	request(t, srv, http.MethodGet, base+"/files/content?path="+url.QueryEscape(files.Items[0].Path), "", http.StatusOK, &file)
	if file.Content == nil || *file.Content != "content from worker\n" {
		t.Fatalf("job snapshot file content: %+v", file)
	}
}

func TestLiveSnapshotSync(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		source    domain.RepositorySourceType
	}{
		{"local", "TEST_SCAN_LOCAL_REPO", domain.RepositorySourceLocalGit},
		{"remote", "TEST_SCAN_REMOTE_REPO", domain.RepositorySourceGitHub},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := os.Getenv(tc.env)
			if source == "" {
				t.Skip(tc.env + " is unset")
			}
			f := newSyncFixture(t)
			repo := domain.Repository{SourceType: tc.source, Source: source}
			if tc.source == domain.RepositorySourceLocalGit {
				dir, err := filepath.Abs(source)
				if err != nil {
					t.Fatal(err)
				}
				repo.Source, repo.LocalPath = dir, &dir
			} else {
				owner, name, canonical, err := github.ParsePublicRepoURL(source)
				if err != nil {
					t.Fatal(err)
				}
				githubID := int64(1)
				repo.GitHubOwner, repo.GitHubRepo, repo.GitHubRepoID = &owner, &name, &githubID
				repo.RemoteURL = canonical
			}
			f.attach(t, repo)
			first := f.sync(t, "")
			if first.Status != domain.SyncOutcomePublished || first.Version != 1 || first.BlobsRead == 0 || first.AddedFiles == 0 {
				t.Fatalf("first live sync: %+v", first)
			}
			second := f.sync(t, "")
			if second.Status != domain.SyncOutcomeUnchanged || second.Version != 1 || second.BlobsRead != 0 || second.SnapshotID != first.SnapshotID {
				t.Fatalf("second live sync: %+v", second)
			}
			page := f.search(t, domain.SearchFilesFilter{Limit: 1})
			if len(page.Items) != 1 {
				t.Fatalf("live search: %+v", page)
			}
			file := page.Items[0]
			filtered := f.search(t, domain.SearchFilesFilter{Query: file.Path, PathPrefix: file.Path, Limit: 1})
			if filtered.Total < 1 || len(filtered.Items) != 1 || filtered.Items[0].Path != file.Path ||
				filtered.Items[0].SnapshotID != first.SnapshotID {
				t.Fatalf("live path search: %+v", filtered)
			}
		})
	}
}

func TestScopedSyncRequests(t *testing.T) {
	f := localSyncFixture(t)
	f.write(t, "README.md", "searchable")
	f.commit(t, "initial")
	reg := &worker.Registry{}
	if err := worker.RegisterSyncHandler(reg, f.svc); err != nil {
		t.Fatal(err)
	}
	store := repository.NewPostgresJobStore(f.pool)
	jobs := service.NewJobService(store, reg, f.cfg.Worker)
	srv := httptest.NewServer(router.New(f.cfg, f.log, router.Dependencies{Jobs: handler.NewJobHandler(jobs, f.log)}))
	defer srv.Close()
	path := "/api/v1/projects/" + f.projectID.String() + "/repositories/" + f.repoID.String() + "/sync"
	var wg sync.WaitGroup
	statuses := make(chan int, 12)
	for range cap(statuses) {
		wg.Go(func() {
			res, err := http.Post(srv.URL+path, "application/json", nil)
			if err != nil {
				t.Error(err)
				return
			}
			res.Body.Close()
			statuses <- res.StatusCode
		})
	}
	wg.Wait()
	close(statuses)
	created := 0
	for s := range statuses {
		if s == 202 {
			created++
		} else if s != 200 {
			t.Fatal(s)
		}
	}
	if created != 1 {
		t.Fatalf("created %d", created)
	}
	var got struct {
		ID  uuid.UUID `json:"job_id"`
		URL string    `json:"poll_url"`
	}
	request(t, srv, "POST", path, "{}", 200, &got)
	request(t, srv, "POST", path, `{"ref":"other"}`, 409, nil)
	request(t, srv, "POST", path, `{"source":"/tmp"}`, 400, nil)
	request(t, srv, "POST", path, `{"ref":"bad//ref"}`, 400, nil)
	request(t, srv, "POST", strings.Replace(path, f.projectID.String(), uuid.NewString(), 1), "", 404, nil)
	startJobWorker(t, f.cfg.Worker, store, reg)
	waitJob(t, store, got.ID, jobStatus(domain.JobSucceeded))
	request(t, srv, "GET", got.URL, "", 200, nil)
	request(t, srv, "POST", path, "", 202, nil)
}
