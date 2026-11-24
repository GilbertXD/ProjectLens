package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"projectlens/internal/domain"
	"projectlens/internal/gitscan"
	"projectlens/internal/handler"
	"projectlens/internal/repository"
	"projectlens/internal/router"
	"projectlens/internal/service"
)

type syncTxObserver func(context.Context, func(context.Context) error) error

func (observe syncTxObserver) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	return observe(ctx, fn)
}

func TestHistoryReachabilityAndChanges(t *testing.T) {
	f := localSyncFixture(t)
	ctx := t.Context()
	f.write(t, "one", "line 1\n")
	first := f.commit(t, "first")
	syncGit(t, f.dir, "checkout", "-b", "side")
	f.write(t, "side", "side")
	side := f.commit(t, "side")
	syncGit(t, f.dir, "checkout", "main")
	syncGit(t, f.dir, "mv", "one", "two")
	f.write(t, "binary", "hello\x00world")
	rename := f.commit(t, "rename")
	syncGit(t, f.dir, "merge", "--no-ff", "-m", "merge", "side")
	shim := t.TempDir()
	marker := filepath.Join(shim, "transaction-active")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ -e "$PROJECTLENS_TEST_HISTORY_TX" ]; then
	echo "Git invoked inside the history transaction" >&2
	exit 1
fi
exec "$PROJECTLENS_TEST_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJECTLENS_TEST_GIT", git)
	t.Setenv("PROJECTLENS_TEST_HISTORY_TX", marker)
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	tx := syncTxObserver(func(ctx context.Context, fn func(context.Context) error) error {
		return f.tx.WithinTx(ctx, func(ctx context.Context) error {
			if err := os.WriteFile(marker, nil, 0600); err != nil {
				return err
			}
			defer func() {
				if err := os.Remove(marker); err != nil {
					t.Error(err)
				}
			}()
			return fn(ctx)
		})
	})
	h := service.HistoryService{Repositories: f.repos, Locks: f.store, Scanner: f.scanner, Tx: tx}
	target := domain.SyncTarget{ProjectID: f.projectID, RepositoryID: f.repoID, Ref: "main"}
	syncCount := func(n int) {
		t.Helper()
		result, err := h.Sync(ctx, target)
		if err != nil || result.Imported != n {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	// A publish failure must also roll back the immutable objects inserted in that batch.
	if _, err := f.pool.Exec(ctx, "ALTER TABLE repository_history_refs ADD CONSTRAINT injected_failure CHECK (false)"); err != nil {
		t.Fatal(err)
	}
	var publicationErr *pgconn.PgError
	if _, err := h.Sync(ctx, target); !errors.As(err, &publicationErr) || publicationErr.ConstraintName != "injected_failure" {
		t.Fatalf("expected injected publication failure after scanning Git, got %v", err)
	}
	var leaked int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM repository_commits").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal(leaked, err)
	}
	if _, err := f.pool.Exec(ctx, "ALTER TABLE repository_history_refs DROP CONSTRAINT injected_failure"); err != nil {
		t.Fatal(err)
	}
	syncCount(4)
	syncCount(0)
	read := func(op, sha, base string) string {
		t.Helper()
		v, err := h.Read(ctx, target, op, sha, base, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		buf, _ := json.Marshal(v)
		return string(buf)
	}
	if v := read("files", rename, ""); !strings.Contains(v, "old_path") || !strings.Contains(v, "binary\":true") {
		t.Fatal(v)
	}
	if v := read("compare", rename, first); !strings.Contains(v, "two") {
		t.Fatal(v)
	}
	if v := read("list", "", ""); !strings.Contains(v, side) {
		t.Fatal(v)
	}
	syncGit(t, f.dir, "reset", "--hard", first)
	f.write(t, "one", "changed")
	f.commit(t, "rewrite")
	syncCount(1)
	if v := read("list", "", ""); strings.Contains(v, side) {
		t.Fatal(v)
	}
	if v := read("detail", side, ""); !strings.Contains(v, fmt.Sprintf("%q", side)) {
		t.Fatal(v)
	}

	syncGit(t, f.dir, "rm", "one")
	for i := range 501 {
		f.write(t, fmt.Sprintf("large/%d", i), strings.Repeat("diff line\n", 40))
	}
	large := f.commit(t, "large diff")
	syncCount(1)
	var compared struct {
		Truncated      bool
		Patch          string
		PatchTruncated bool `json:"patch_truncated"`
	}
	if err := json.Unmarshal([]byte(read("compare", large, first)), &compared); err != nil || !compared.Truncated || !compared.PatchTruncated || len(compared.Patch) > 64<<10 {
		t.Fatal("compare did not cap patch", err)
	}
	if v := read("files", large, ""); !strings.Contains(v, `"truncated":true`) {
		t.Fatal("missing file truncation")
	}
}

func TestHistoryPages(t *testing.T) {
	f := localSyncFixture(t)
	var stream strings.Builder
	for i := 1; i <= 102; i++ {
		fmt.Fprintf(&stream, "commit refs/heads/main\nmark :%d\ncommitter Fixture <test@example.org> %d +0000\ndata 1\nx\n", i, 1700000000+i)
		if i > 1 {
			fmt.Fprintf(&stream, "from :%d\n", i-1)
		}
		stream.WriteByte('\n')
	}
	cmd := exec.CommandContext(t.Context(), "git", "-C", f.dir, "fast-import", "--quiet")
	cmd.Stdin = strings.NewReader(stream.String())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	svc := service.HistoryService{Repositories: f.repos, Locks: f.store, Scanner: f.scanner, Tx: f.tx}
	target := domain.SyncTarget{RepositoryID: f.repoID, ProjectID: f.projectID, Ref: "main"}
	for i, n := range []int{100, 2, 0} {
		page, err := svc.Sync(t.Context(), target)
		more := i == 0
		if err != nil || page.Imported != n || page.HasMore != more ||
			page.HistoryWindow != gitscan.DescribeHistoryWindow(page.ReachableCount, more) ||
			page.ReachableCount != min((i+1)*gitscan.HistoryPage, 102) {
			t.Fatal(page, err)
		}
		raw, err := f.repos.HistoryList(t.Context(), f.repoID, "main", 1, 0)
		var stored gitscan.HistoryWindow
		if err != nil || json.Unmarshal(raw, &stored) != nil || stored != page.HistoryWindow {
			t.Fatalf("stored window = %s, %v, want %+v", raw, err, page.HistoryWindow)
		}
	}
	result, err := f.repos.HistoryList(t.Context(), f.repoID, "main", 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []json.RawMessage
		Total int
	}
	if err := json.Unmarshal(result, &page); err != nil || page.Total != 102 || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
}

func TestHistoryImportRepairsShallowBoundary(t *testing.T) {
	f := localSyncFixture(t)
	f.write(t, "file.txt", "first\n")
	f.commit(t, "first")
	f.write(t, "file.txt", "first\nsecond\n")
	tip := f.commit(t, "second")
	shallow := filepath.Join(t.TempDir(), "shallow")
	syncGit(t, f.dir, "clone", "--depth=1", "--", "file://"+f.dir, shallow)
	f.attach(t, domain.Repository{SourceType: domain.RepositorySourceLocalGit, Source: shallow, LocalPath: &shallow})
	svc := service.HistoryService{Repositories: f.repos, Locks: f.store, Scanner: f.scanner, Tx: f.tx}
	target := domain.SyncTarget{RepositoryID: f.repoID, ProjectID: f.projectID, Ref: "main"}
	ctx := t.Context()

	check := func(status string, truncated bool, total int) {
		t.Helper()
		page, err := svc.Sync(ctx, target)
		if err != nil || page.WindowStatus != status || page.Imported != 1 {
			t.Fatalf("sync result = %+v, %v", page, err)
		}
		raw, err := f.repos.Commit(ctx, f.repoID, tip, true, 20, 0)
		var files struct {
			Truncated bool
			Total     int
		}
		if err != nil || json.Unmarshal(raw, &files) != nil || files.Truncated != truncated || files.Total != total {
			t.Fatalf("commit files = %s, %v", raw, err)
		}
	}
	check("partial", true, 0)
	syncGit(t, shallow, "fetch", "--unshallow")
	check("complete", false, 1)
}

func TestHistoricalQueryEndpointsPreserveSnapshots(t *testing.T) {
	f := localSyncFixture(t)
	ctx := t.Context()
	f.write(t, "src/before.txt", "first\nsecond\n")
	f.write(t, "binary", "a\x00b")
	f.write(t, "large", strings.Repeat("a", int(f.cfg.Scanner.MaxFileSize)+1))
	first := f.commit(t, "original")
	syncGit(t, f.dir, "mv", "src/before.txt", "src/after.txt")
	renamed := f.commit(t, "rename")
	f.write(t, "src/after.txt", "first\nchanged\n")
	tip := f.commit(t, "update")
	syncGit(t, f.dir, "tag", "-a", "v1", "-m", "release", first)
	tag := syncGit(t, f.dir, "rev-parse", "refs/tags/v1")

	localID := f.repoID
	localHead := syncGit(t, f.dir, "rev-parse", "HEAD")
	f.write(t, "src/after.txt", "dirty worktree\n")
	localStatus := syncGit(t, f.dir, "status", "--porcelain")

	web := t.TempDir()
	bare := filepath.Join(web, "owner", "project.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0700); err != nil {
		t.Fatal(err)
	}
	syncGit(t, f.dir, "clone", "--bare", "--", f.dir, bare)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	var unavailable atomic.Int32
	backend := &cgi.Handler{Path: git, Dir: web, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + web, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}}
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code := unavailable.Load(); code != 0 {
			w.WriteHeader(int(code))
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(mirror.Close)
	f.cfg.GitHub.GitBaseURL = mirror.URL
	f.cfg.GitHub.Token = ""
	const remote = "https://github.com/owner/project"
	owner, name := "owner", "project"
	githubRepoID := int64(42)
	f.attach(t, domain.Repository{SourceType: domain.RepositorySourceGitHub, Source: remote, RemoteURL: remote,
		GitHubOwner: &owner, GitHubRepo: &name, GitHubRepoID: &githubRepoID})

	history := &service.HistoryService{Repositories: f.repos, Locks: f.store, Scanner: f.scanner, Tx: f.tx}
	api := router.New(f.cfg, f.log, router.Dependencies{
		History: &handler.HistoryHandler{Service: history, Log: f.log},
	})
	foreign, err := service.NewProjectService(repository.NewPostgresProjectRepository(f.pool), f.tx).
		Create(ctx, domain.CreateProjectInput{Slug: "foreign-history", Name: "Foreign History"})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, code int, data any) {
		t.Helper()
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != code {
			t.Fatalf("GET %s = %d %s, want %d", path, recorder.Code, recorder.Body, code)
		}
		if data != nil {
			if err := json.Unmarshal(recorder.Body.Bytes(), data); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshotState := func(repoID uuid.UUID) string {
		t.Helper()
		var state string
		err := f.pool.QueryRow(ctx, `
			SELECT jsonb_build_object(
				'snapshots', (SELECT jsonb_agg(to_jsonb(s) ORDER BY s.id) FROM snapshots s WHERE repository_id=$1),
				'files', (SELECT jsonb_agg(to_jsonb(f) ORDER BY f.id) FROM source_files f WHERE repository_id=$1)
			)::text`, repoID).Scan(&state)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	for _, id := range []uuid.UUID{localID, f.repoID} {
		snapshot, err := f.svc.SyncRepository(ctx, f.projectID, id, "main", nil)
		if err != nil || snapshot.CommitHash != tip {
			t.Fatalf("active snapshot = %+v, %v", snapshot, err)
		}
		before := snapshotState(id)
		base := projects + "/" + f.projectID.String() + "/repositories/" + id.String()
		forbidden := projects + "/" + foreign.ID.String() + "/repositories/" + id.String()
		treePath := "/commits/" + first + "/tree?path_prefix=src%2F&limit=1"
		filePath := "/commits/" + first + "/file?path=" + url.QueryEscape("src/before.txt")
		historyPath := "/files/history?path=src%2Fafter.txt&ref=main&follow=true&limit=2"
		blamePath := "/files/blame?path=src%2Fafter.txt&ref=" + tip + "&start_line=1&end_line=2"
		refsPath := "/refs?type=tag"
		for _, path := range []string{treePath, filePath, historyPath, blamePath, refsPath} {
			get(forbidden+path, http.StatusNotFound, nil)
		}

		// Query historical objects before importing commit history into Postgres.
		var tree gitscan.CommitTreeResult
		get(base+treePath, http.StatusOK, &tree)
		if tree.CommitHash != first || tree.Total != 1 || len(tree.Items) != 1 || tree.Items[0].Path != "src/before.txt" {
			t.Fatalf("tree = %+v", tree)
		}
		var file gitscan.CommitFileResult
		get(base+filePath, http.StatusOK, &file)
		if file.Content != "first\nsecond\n" || file.CommitHash != first || file.LineCount != 2 {
			t.Fatalf("file = %+v", file)
		}
		var changes gitscan.FileHistoryResult
		get(base+historyPath, http.StatusOK, &changes)
		if !changes.HasMore || !changes.Truncated || changes.Shallow || len(changes.Items) != 2 ||
			changes.Items[0].Hash != tip || changes.Items[1].Hash != renamed || changes.Items[1].OldPath != "src/before.txt" {
			t.Fatalf("history = %+v", changes)
		}
		get(base+historyPath+"&offset=2", http.StatusOK, &changes)
		if changes.HasMore || !changes.HistoryComplete || len(changes.Items) != 1 || changes.Items[0].Hash != first {
			t.Fatalf("second history page = %+v", changes)
		}
		var blame gitscan.FileBlameResult
		get(base+blamePath, http.StatusOK, &blame)
		if len(blame.Lines) != 2 || blame.Lines[0].CommitHash != first || blame.Lines[0].Filename != "src/before.txt" ||
			blame.Lines[1].CommitHash != tip || blame.Lines[1].Content != "changed" {
			t.Fatalf("blame = %+v", blame)
		}
		var refs gitscan.RefListResult
		get(base+refsPath, http.StatusOK, &refs)
		if refs.DefaultBranch != "main" || refs.Total != 1 || len(refs.Items) != 1 ||
			refs.Items[0].ObjectHash != tag || refs.Items[0].CommitHash != first {
			t.Fatalf("refs = %+v", refs)
		}

		get(base+"/commits/"+first+"/file?path=missing", http.StatusNotFound, nil)
		get(base+"/commits/"+strings.Repeat("0", 40)+"/tree", http.StatusNotFound, nil)
		get(base+"/commits/"+first+"/file?path=large", http.StatusRequestEntityTooLarge, nil)
		get(base+"/commits/"+first+"/file?path=%2E%2E%2Fsecret", http.StatusBadRequest, nil)
		get(base+"/files/blame?path=binary", http.StatusBadRequest, nil)
		if id == f.repoID {
			unavailable.Store(http.StatusServiceUnavailable)
			get(base+filePath, http.StatusOK, nil)
			get(base+refsPath, http.StatusBadGateway, nil)
			unavailable.Store(http.StatusTooManyRequests)
			get(base+refsPath, http.StatusTooManyRequests, nil)
			unavailable.Store(0)
		}

		page, err := history.Sync(ctx, domain.SyncTarget{ProjectID: f.projectID, RepositoryID: id, Ref: "main"})
		if err != nil || page.Imported != 3 || page.ReachableCount != 3 ||
			page.WindowStatus != "complete" || !page.Complete || page.Truncated || page.WindowLimitReached {
			t.Fatalf("published history = %+v, %v", page, err)
		}
		var window gitscan.HistoryWindow
		get(base+"/commits?ref=main", http.StatusOK, &window)
		if window != page.HistoryWindow || window.WindowLimit != gitscan.HistoryLimit || window.PageSize != gitscan.HistoryPage {
			t.Fatalf("commit listing window = %+v, want %+v", window, page.HistoryWindow)
		}
		if snapshotState(id) != before {
			t.Fatal("historical queries or commit import changed snapshots or source_files")
		}
	}
	if syncGit(t, f.dir, "rev-parse", "HEAD") != localHead || syncGit(t, f.dir, "status", "--porcelain") != localStatus ||
		syncGit(t, bare, "rev-parse", "HEAD") != tip {
		t.Fatal("repository HEAD or local worktree changed")
	}
}
