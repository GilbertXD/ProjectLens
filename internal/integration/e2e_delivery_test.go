package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"projectlens/internal/cache"
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

const deliveryToken = "delivery-auth-fixture"

func TestE2EDelivery(t *testing.T) {
	cfg := database(t)
	base, err := config.Load("../../configs/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server, cfg.Worker, cfg.Document, cfg.Sync = base.Server, base.Worker, base.Document, base.Sync
	cfg.Auth = config.AuthConfig{Enabled: true, BearerTokens: []string{deliveryToken}}
	cfg.Worker.PollInterval = 20 * time.Millisecond
	cfg.Worker.RetryBaseDelay = 20 * time.Millisecond
	cfg.Worker.RetryMaxDelay = 100 * time.Millisecond

	local, localCommit := deliveryGitRepo(t, "local.txt", "deliveryquasar from the local work tree")
	remote, remoteCommit := deliveryGitRepo(t, "remote.txt", "deliveryquasar from the GitHub mirror")
	mirrorRoot := t.TempDir()
	bare := filepath.Join(mirrorRoot, "owner", "project.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0700); err != nil {
		t.Fatal(err)
	}
	syncGit(t, remote, "clone", "--bare", remote, bare)
	syncGit(t, bare, "update-server-info")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/repos/owner/project", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":42,"full_name":"owner/project","name":"project","visibility":"public","default_branch":"main","html_url":"https://github.com/owner/project"}`)
	})
	mux.Handle("/git/", http.StripPrefix("/git/", http.FileServer(http.Dir(mirrorRoot))))
	mirror := httptest.NewServer(mux)
	defer mirror.Close()
	cfg.GitHub = config.GitHubConfig{APIBaseURL: mirror.URL + "/api", GitBaseURL: mirror.URL + "/git", Timeout: time.Second, UserAgent: "ProjectLens E2E"}
	cfg.Scanner = config.DefaultScannerConfig()
	cfg.Scanner.CacheDir = t.TempDir()
	cfg.Repository.AllowedLocalRoots = []string{local}

	redis := miniredis.RunT(t)
	cfg.Redis = searchRedisConfig(redis.Addr())
	srv, stop := startDeliveryStack(t, cfg)
	defer func() { stop() }()
	deliveryRequest(t, srv, "GET", "/healthz", "", false, 200, nil)
	checkDeliveryReadiness(t, srv, "ok")
	for _, format := range []string{"yaml", "json"} {
		headers := deliveryRequest(t, srv, "GET", "/openapi."+format, "", false, 200, nil)
		if !strings.HasPrefix(headers.Get("Content-Type"), "application/"+format) {
			t.Fatalf("OpenAPI headers: %v", headers)
		}
	}
	deliveryRequest(t, srv, "GET", "/api/v1/ping", "", false, 401, nil)
	deliveryRequest(t, srv, "POST", projects, strings.Repeat("x", int(cfg.Server.MaxBodyBytes)+1), true, 413, nil)

	var project domain.Project
	deliveryRequest(t, srv, "POST", projects, `{"slug":"delivery","name":"Delivery"}`, true, 201, &project)
	projectPath := projects + "/" + project.ID.String()
	repoPath := projectPath + "/repositories"
	deliveryRequest(t, srv, "POST", repoPath, `{"source_type":"local_git","source":"/unapproved"}`, true, 403, nil)
	commits := make(map[domain.SearchSourceType]string)
	repositoryIDs := make(map[domain.SearchSourceType]string)
	for _, repo := range []struct{ source, kind, commit string }{
		{local, "local_git", localCommit}, {"https://github.com/owner/project", "github", remoteCommit},
	} {
		var linked domain.Repository
		deliveryRequest(t, srv, "POST", repoPath, fmt.Sprintf(`{"source_type":%q,"source":%q}`, repo.kind, repo.source), true, 201, &linked)
		finished := deliveryJob(t, srv, fmt.Sprintf(`{"type":"repository.sync","payload":{"project_id":%q,"repository_id":%q,"ref":"main"}}`, project.ID, linked.ID))
		var result domain.SyncResult
		if err := json.Unmarshal(finished.Result, &result); err != nil || result.Status != domain.SyncOutcomePublished || result.CommitHash != repo.commit {
			t.Fatalf("sync job result = %s, %v", finished.Result, err)
		}
		commits[domain.SearchSourceType(repo.kind)] = repo.commit
		repositoryIDs[domain.SearchSourceType(repo.kind)] = linked.ID.String()
	}
	query := searchPath(project.ID, "deliveryquasar")
	before := deliverySearch(t, srv, query, "miss")
	if before.Total != 2 {
		t.Fatalf("expected two indexed repositories: %+v", before)
	}
	deliverySearch(t, srv, query, "hit")

	var doc domain.Document
	headers := deliveryRequest(t, srv, "POST", projectPath+"/documents", `{"title":"Guide","content":"# Search\nDeliveryquasar from Markdown."}`, true, 201, &doc)
	if headers.Get("ETag") != `"1"` {
		t.Fatal("document create omitted version ETag")
	}
	fresh := deliverySearch(t, srv, query, "miss")
	if fresh.Total != 3 || len(fresh.Items) != 3 || fresh.SearchVersion <= before.SearchVersion {
		t.Fatalf("document did not invalidate search: %+v", fresh)
	}
	seen := map[domain.SearchSourceType]bool{}
	for _, hit := range fresh.Items {
		if hit.ProjectID != project.ID || hit.Location.APIPath == "" || hit.Location.Line == nil {
			t.Fatalf("missing provenance: %+v", hit)
		}
		switch hit.SourceType {
		case domain.SearchSourceDocument:
			if hit.Location.DocumentID == nil || *hit.Location.DocumentID != doc.ID || hit.Location.DocumentVersion == nil || *hit.Location.DocumentVersion != 1 {
				t.Fatalf("invalid document provenance: %+v", hit)
			}
		case domain.SearchSourceLocalGit, domain.SearchSourceGitHub:
			if hit.Location.RepositoryID == nil || hit.Location.RepositoryID.String() != repositoryIDs[hit.SourceType] ||
				hit.Location.SnapshotID == nil || hit.Location.CommitHash != commits[hit.SourceType] {
				t.Fatalf("invalid repository provenance: %+v", hit)
			}
			if hit.SourceType == domain.SearchSourceGitHub && !strings.Contains(hit.Location.URL, "https://github.com/owner/project/blob/"+remoteCommit+"/remote.txt") {
				t.Fatalf("invalid canonical GitHub location: %+v", hit)
			}
		default:
			t.Fatalf("unexpected source %q", hit.SourceType)
		}
		seen[hit.SourceType] = true
	}
	if len(seen) != 3 || !reflect.DeepEqual(deliverySearch(t, srv, query, "hit").Items, fresh.Items) {
		t.Fatal("search sources or cached results differ")
	}

	redis.Close()
	checkDeliveryReadiness(t, srv, "degraded")
	if degraded := deliverySearch(t, srv, query, "degraded"); !reflect.DeepEqual(degraded.Items, fresh.Items) {
		t.Fatal("Redis outage changed search results")
	}
	if err := redis.Restart(); err != nil {
		t.Fatal(err)
	}
	redis.FlushAll()
	checkDeliveryReadiness(t, srv, "ok")
	deliverySearch(t, srv, query, "miss")
	deliverySearch(t, srv, query, "hit")
	flaky := deliveryJob(t, srv, `{"type":"mock.flaky","max_attempts":3,"payload":{"succeed_on_attempt":2}}`)
	if flaky.Attempt != 2 {
		t.Fatalf("expected recovery on attempt 2: %+v", flaky)
	}

	stop()
	srv, stop = startDeliveryStack(t, cfg)
	checkDeliveryReadiness(t, srv, "ok")
	var restored domain.Project
	deliveryRequest(t, srv, "GET", projectPath, "", true, 200, &restored)
	if restored != project {
		t.Fatalf("project changed across restart: %+v", restored)
	}
	if after := deliverySearch(t, srv, query, "hit"); !reflect.DeepEqual(after.Items, fresh.Items) ||
		after.SearchVersion != fresh.SearchVersion {
		t.Fatal("search data changed after restart")
	}
}

func deliveryGitRepo(t *testing.T, filename, content string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	syncGit(t, dir, "init", "--initial-branch=main", "--template=")
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	syncGit(t, dir, "add", "--", filename)
	syncGit(t, dir, "commit", "-m", "fixture content")
	return dir, syncGit(t, dir, "rev-parse", "HEAD")
}

func startDeliveryStack(t *testing.T, cfg config.Config) (*httptest.Server, func()) {
	t.Helper()
	pool := openPool(t, cfg)
	t.Cleanup(pool.Close)
	if err := db.MigrateUp(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	snapshots, err := repository.NewPostgresSnapshotStore(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshots.Close)
	redis := cache.NewRedisSearchCache(cfg.Redis)
	t.Cleanup(func() { _ = redis.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tx := db.NewPgxTxManager(pool)
	projects := repository.NewPostgresProjectRepository(pool)
	repos := repository.NewPostgresRepositoryStore(pool)
	jobs := repository.NewPostgresJobStore(pool)
	scanner := gitscan.NewScanner(cfg.Scanner, cfg.Repository, cfg.GitHub)
	syncer := service.NewSyncService(repos, snapshots, tx, scanner, cfg.Sync, log)
	registry := &worker.Registry{}
	if err := worker.RegisterSyncHandler(registry, syncer); err != nil {
		t.Fatal(err)
	}
	if err := worker.RegisterMockHandlers(registry); err != nil {
		t.Fatal(err)
	}
	engine := router.New(cfg, log, router.Dependencies{
		Projects:     handler.NewProjectHandler(service.NewProjectService(projects, tx), log),
		Documents:    handler.NewDocumentHandler(service.NewDocumentService(projects, repository.NewPostgresDocumentStore(pool), cfg.Document.MaxContentBytes), log, cfg.Document.MaxContentBytes),
		Repositories: handler.NewRepositoryHandler(service.NewRepositoryService(projects, repos, gitrepo.NewLocalInspector(cfg.Repository.AllowedLocalRoots), github.NewClient(cfg.GitHub, nil)), log),
		Snapshots:    handler.NewSnapshotHandler(syncer, log),
		Search:       handler.NewSearchHandler(service.NewSearchService(repository.NewPostgresSearchStore(pool), redis, cfg.Search, cfg.Redis.KeyPrefix, log), log),
		Jobs:         handler.NewJobHandler(service.NewJobService(jobs, registry, cfg.Worker), log),
		Pinger:       pool, CachePinger: redis,
	})
	srv := httptest.NewServer(engine)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.NewRunner(jobs, registry, cfg.Worker, log).Run(ctx)
	}()
	return srv, func() {
		srv.Close()
		cancel()
		<-done
		_ = redis.Close()
		snapshots.Close()
		pool.Close()
	}
}

func deliveryRequest(t *testing.T, srv *httptest.Server, method, path, body string, auth bool, status int, out any) http.Header {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+deliveryToken)
	}
	response, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != status {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("%s %s returned %d, want %d: %s", method, path, response.StatusCode, status, data)
	}
	if out != nil {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
	return response.Header
}

func checkDeliveryReadiness(t *testing.T, srv *httptest.Server, redis string) {
	t.Helper()
	var ready struct {
		Status string
		Checks map[string]string
	}
	deliveryRequest(t, srv, "GET", "/readyz", "", false, 200, &ready)
	if ready.Status != "ok" || ready.Checks["postgres"] != "ok" || ready.Checks["redis"] != redis {
		t.Fatalf("readiness = %+v, want Redis %s", ready, redis)
	}
}

func deliverySearch(t *testing.T, srv *httptest.Server, path, status string) domain.SearchResult {
	t.Helper()
	var result domain.SearchResult
	h := deliveryRequest(t, srv, "GET", path, "", true, 200, &result)
	if h.Get("X-Search-Cache") != status || string(result.CacheStatus) != status ||
		h.Get("X-Search-Version") != fmt.Sprint(result.SearchVersion) {
		t.Fatalf("search cache headers = %v, want status %s. Result: %+v", h, status, result)
	}
	return result
}

func deliveryJob(t *testing.T, srv *httptest.Server, payload string) domain.Job {
	t.Helper()
	const path = "/api/v1/jobs"
	var job domain.Job
	deliveryRequest(t, srv, "POST", path, payload, true, 202, &job)
	until := time.Now().Add(15 * time.Second)
	for time.Now().Before(until) {
		deliveryRequest(t, srv, "GET", path+"/"+job.ID.String(), "", true, 200, &job)
		switch job.Status {
		case domain.JobSucceeded:
			return job
		case domain.JobFailed, domain.JobCancelled:
			t.Fatalf("job did not succeed: %+v", job)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job still incomplete: %+v", job)
	return job
}
