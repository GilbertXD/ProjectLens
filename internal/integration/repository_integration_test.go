package integration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/github"
	"projectlens/internal/gitrepo"
	"projectlens/internal/gitscan"
	"projectlens/internal/handler"
	"projectlens/internal/repository"
	"projectlens/internal/router"
	"projectlens/internal/service"
	"projectlens/internal/worker"
)

func TestRepositoryLifecycle(t *testing.T) {
	cfg := database(t)
	pool := openPool(t, cfg)
	defer pool.Close()
	if err := db.MigrateUp(t.Context(), pool); err != nil {
		t.Fatal(err)
	}

	var stars atomic.Int32
	stars.Store(2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.ToLower(r.URL.Path) {
		case "/repos/owner/repo":
			fmt.Fprintf(w, `{"id":18,"full_name":"Owner/Repo","visibility":"public","stargazers_count":%d,"language":"Go","default_branch":"main","description":"GitHub project"}`, stars.Load())
		case "/repos/owner/private":
			fmt.Fprint(w, `{"private":true}`)
		case "/repos/owner/limited":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
		case "/repos/owner/unavailable":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	cfg.GitHub = config.GitHubConfig{APIBaseURL: upstream.URL, Timeout: time.Second, UserAgent: "ProjectLens"}
	root := t.TempDir()
	repoPath := filepath.Join(root, "sample")
	if err := os.Mkdir(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoPath}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("remote", "add", "origin", "https://user:secret@example.com/org/sample")
	canonical, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(repoPath, alias); err != nil {
		t.Fatal(err)
	}
	cfg.Repository.AllowedLocalRoots = []string{root}
	srv := startHTTP(cfg, pool)
	defer srv.Close()
	var project, other domain.Project
	request(t, srv, "POST", projects, `{"slug":"one-project","name":"One"}`, 201, &project)
	request(t, srv, "POST", projects, `{"slug":"two-project","name":"Two"}`, 201, &other)
	projectPath := projects + "/" + project.ID.String()
	base, otherBase := projectPath+"/repositories", projects+"/"+other.ID.String()+"/repositories"

	associate := func(sourceType domain.RepositorySourceType, source string) string {
		t.Helper()
		data, err := json.Marshal(domain.AssociateRepositoryInput{SourceType: sourceType, Source: source})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	var local, gh domain.Repository
	request(t, srv, "POST", base, associate(domain.RepositorySourceLocalGit, repoPath), 201, &local)
	if local.ID == uuid.Nil || local.ProjectID != project.ID || local.LocalPath == nil || *local.LocalPath != canonical ||
		local.CanonicalSource != canonical || local.Name != "sample" || local.DefaultBranch != "main" ||
		local.RemoteURL != "https://example.com/org/sample" || local.GitHubOwner != nil {
		t.Fatalf("unexpected local metadata: %+v", local)
	}
	request(t, srv, "POST", base, associate(domain.RepositorySourceLocalGit, alias), 409, nil)
	request(t, srv, "POST", base, associate(domain.RepositorySourceLocalGit, root), 400, nil)
	request(t, srv, "POST", base, associate(domain.RepositorySourceLocalGit, t.TempDir()), 403, nil)
	ghBody := associate(domain.RepositorySourceGitHub, "https://github.com/owner/repo")
	request(t, srv, "POST", base, ghBody, 201, &gh)
	if gh.ID == uuid.Nil || gh.LocalPath != nil || gh.CanonicalSource != "github:owner/repo" || gh.DefaultBranch != "main" ||
		gh.Description != "GitHub project" || gh.RemoteURL != "https://github.com/Owner/Repo" || gh.GitHubOwner == nil || *gh.GitHubOwner != "Owner" ||
		gh.GitHubRepo == nil || *gh.GitHubRepo != "Repo" || gh.GitHubRepoID == nil || *gh.GitHubRepoID != 18 ||
		gh.GitHubVisibility == nil || *gh.GitHubVisibility != "public" || gh.GitHubStars == nil || *gh.GitHubStars != 2 ||
		gh.GitHubLanguage == nil || *gh.GitHubLanguage != "Go" {
		t.Fatalf("unexpected GitHub metadata: %+v", gh)
	}
	request(t, srv, "POST", base, associate(domain.RepositorySourceGitHub, "https://WWW.GITHUB.COM/OWNER/REPO.GIT/"), 409, nil)
	request(t, srv, "POST", otherBase, ghBody, 201, nil)
	for _, tc := range []struct {
		source string
		status int
		code   string
	}{
		{"http://github.com/owner/repo", 400, "invalid_input"},
		{"https://github.com/owner/private", 403, "forbidden"},
		{"https://github.com/owner/missing", 404, "not_found"},
		{"https://github.com/owner/limited", 429, "rate_limited"},
		{"https://github.com/owner/unavailable", 502, "upstream_error"},
	} {
		var response map[string]string
		request(t, srv, "POST", base, associate(domain.RepositorySourceGitHub, tc.source), tc.status, &response)
		if response["code"] != tc.code {
			t.Errorf("%s returned %+v", tc.source, response)
		}
	}

	var list domain.RepositoryListResult
	request(t, srv, "GET", base+"?source_type=github&limit=1", "", 200, &list)
	if list.Total != 1 || list.Limit != 1 || len(list.Items) != 1 || list.Items[0].ID != gh.ID {
		t.Fatalf("unexpected filtered page: %+v", list)
	}
	request(t, srv, "GET", base+"?limit=1&offset=2", "", 200, &list)
	if list.Total != 2 || list.Offset != 2 || list.Items == nil || len(list.Items) != 0 {
		t.Fatalf("unexpected empty page: %+v", list)
	}
	ghPath := base + "/" + gh.ID.String()
	var got domain.Repository
	request(t, srv, "GET", ghPath, "", 200, &got)
	if got.ID != gh.ID {
		t.Fatalf("unexpected repository: %+v", got)
	}
	request(t, srv, "GET", otherBase+"/"+gh.ID.String(), "", 404, nil)
	request(t, srv, "GET", projects+"/"+uuid.NewString()+"/repositories", "", 404, nil)
	request(t, srv, "PATCH", ghPath, `{"name":" Custom ","description":"Notes"}`, 200, &got)
	if got.Name != "Custom" || got.Description != "Notes" {
		t.Fatalf("unexpected patch: %+v", got)
	}
	stars.Store(3)
	request(t, srv, "POST", ghPath+"/refresh-metadata", "", 200, &got)
	if got.GitHubStars == nil || *got.GitHubStars != 3 || got.LastSyncedAt == nil ||
		got.Name != "Custom" || got.Description != "Notes" {
		t.Fatalf("unexpected GitHub sync: %+v", got)
	}
	git("symbolic-ref", "HEAD", "refs/heads/next")
	request(t, srv, "POST", base+"/"+local.ID.String()+"/refresh-metadata", "", 200, &got)
	if got.DefaultBranch != "next" || got.LastSyncedAt == nil {
		t.Fatalf("unexpected local sync: %+v", got)
	}
	request(t, srv, "DELETE", ghPath, "", 204, nil)
	request(t, srv, "GET", ghPath, "", 404, nil)
	request(t, srv, "DELETE", projectPath, "", 204, nil)
	var count int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM repositories WHERE project_id = $1", project.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("project deletion left %d repositories: %v", count, err)
	}
}

func TestReadOnlySourceRepositoryLifecycle(t *testing.T) {
	cfg, pool, jobs, registry, jobService := jobFixture(t)
	cfg.Scanner = config.DefaultScannerConfig()
	cfg.Sync = config.SyncConfig{RetainSuperseded: 2, FailedRetention: 24 * time.Hour}
	dir, base := deliveryGitRepo(t, "tracked.txt", "original")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("updated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	syncGit(t, dir, "add", "--", "tracked.txt")
	syncGit(t, dir, "commit", "-m", "update fixture")
	head := syncGit(t, dir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("work tree only\n"), 0600); err != nil {
		t.Fatal(err)
	}

	type sourceEntry struct {
		Mode os.FileMode
		Hash [sha256.Size]byte
	}
	capture := func(makeReadOnly bool) map[string]sourceEntry {
		t.Helper()
		entries := make(map[string]sourceEntry)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			mode := info.Mode()
			if makeReadOnly && mode&os.ModeSymlink == 0 {
				original := mode.Perm()
				mode = mode.Type() | 0400
				if d.IsDir() {
					mode |= 0100
				}
				if err := os.Chmod(path, mode.Perm()); err != nil {
					return err
				}
				t.Cleanup(func() {
					if err := os.Chmod(path, original); err != nil {
						t.Error(err)
					}
				})
			}
			var data []byte
			switch {
			case mode.IsRegular():
				data, err = os.ReadFile(path)
			case mode&os.ModeSymlink != 0:
				var target string
				target, err = os.Readlink(path)
				data = []byte(target)
			}
			if err != nil {
				return err
			}
			entries[path] = sourceEntry{Mode: mode, Hash: sha256.Sum256(data)}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
	baseline := capture(true)
	assertIntact := func(stage string) {
		t.Helper()
		actual := capture(false)
		if reflect.DeepEqual(baseline, actual) {
			return
		}
		for path, want := range baseline {
			got, exists := actual[path]
			if !exists {
				t.Errorf("%s removed %s", stage, path)
			} else if want != got {
				t.Errorf("%s changed %s", stage, path)
			}
		}
		for path := range actual {
			if _, exists := baseline[path]; !exists {
				t.Errorf("%s added %s", stage, path)
			}
		}
		t.FailNow()
	}

	cfg.Repository.AllowedLocalRoots = []string{dir}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tx := db.NewPgxTxManager(pool)
	projectStore := repository.NewPostgresProjectRepository(pool)
	repos := repository.NewPostgresRepositoryStore(pool)
	snapshots, err := repository.NewPostgresSnapshotStore(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshots.Close)
	scanner := gitscan.NewScanner(cfg.Scanner, cfg.Repository, cfg.GitHub)
	syncer := service.NewSyncService(repos, snapshots, tx, scanner, cfg.Sync, log)
	history := &service.HistoryService{Repositories: repos, Locks: snapshots, Scanner: scanner, Tx: tx}
	if err := worker.RegisterSyncHandler(registry, syncer); err != nil {
		t.Fatal(err)
	}
	if err := worker.RegisterTarget(registry, "repository.commits.sync", history.Sync); err != nil {
		t.Fatal(err)
	}
	startJobWorker(t, cfg.Worker, jobs, registry)
	srv := httptest.NewServer(router.New(cfg, log, router.Dependencies{
		Projects: handler.NewProjectHandler(service.NewProjectService(projectStore, tx), log),
		Repositories: handler.NewRepositoryHandler(service.NewRepositoryService(projectStore, repos,
			gitrepo.NewLocalInspector(cfg.Repository.AllowedLocalRoots), github.NewClient(cfg.GitHub, nil)), log),
		Snapshots: handler.NewSnapshotHandler(syncer, log),
		History:   &handler.HistoryHandler{Service: history, Log: log},
		Jobs:      handler.NewJobHandler(jobService, log),
		Pinger:    pool,
	}))
	t.Cleanup(srv.Close)

	var project domain.Project
	request(t, srv, "POST", projects, `{"slug":"source-readonly","name":"Read-only source"}`, 201, &project)
	projectPath := projects + "/" + project.ID.String()
	association, err := json.Marshal(domain.AssociateRepositoryInput{SourceType: domain.RepositorySourceLocalGit, Source: dir})
	if err != nil {
		t.Fatal(err)
	}
	var repo domain.Repository
	request(t, srv, "POST", projectPath+"/repositories", string(association), 201, &repo)
	repoPath := projectPath + "/repositories/" + repo.ID.String()
	for _, suffix := range []string{"/sync", "/commits/sync"} {
		var queued struct {
			JobID uuid.UUID `json:"job_id"`
		}
		request(t, srv, "POST", repoPath+suffix, `{"ref":"main"}`, 202, &queued)
		job := waitJob(t, jobs, queued.JobID, func(j domain.Job) bool {
			return j.Status == domain.JobSucceeded || j.Status == domain.JobFailed || j.Status == domain.JobCancelled
		})
		if job.Status != domain.JobSucceeded {
			t.Fatalf("%s failed: %+v", suffix, job)
		}
	}
	var snapshot domain.Snapshot
	request(t, srv, "GET", repoPath+"/snapshots/active", "", 200, &snapshot)
	if snapshot.CommitHash != head {
		t.Fatalf("active snapshot commit = %s, want %s", snapshot.CommitHash, head)
	}
	var compare struct {
		Total int    `json:"total"`
		Patch string `json:"patch"`
	}
	request(t, srv, "GET", repoPath+"/compare?base="+base+"&head="+head, "", 200, &compare)
	if compare.Total != 1 || !strings.Contains(compare.Patch, "+updated") {
		t.Fatalf("unexpected comparison: %+v", compare)
	}
	request(t, srv, "POST", repoPath+"/refresh-metadata", "", 200, &repo)
	assertIntact("association, sync, compare and metadata refresh")

	request(t, srv, "DELETE", repoPath, "", 204, nil)
	request(t, srv, "GET", repoPath, "", 404, nil)
	assertIntact("repository association deletion")

	request(t, srv, "POST", projectPath+"/repositories", string(association), 201, &repo)
	request(t, srv, "DELETE", projectPath, "", 204, nil)
	request(t, srv, "GET", projectPath+"/repositories/"+repo.ID.String(), "", 404, nil)
	assertIntact("project deletion with an associated repository")
}
