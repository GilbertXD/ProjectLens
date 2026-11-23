package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/cache"
	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/gitscan"
	"projectlens/internal/repository"
	"projectlens/internal/service"
)

// The sync service publishes a GitHub snapshot from deterministic local Git objects.
type searchGitHubScanner struct {
	scanner *gitscan.Scanner
	path    string
}

func (s searchGitHubScanner) ScanIncremental(ctx context.Context, spec gitscan.SourceSpec, base *gitscan.IncrementalBase, opts gitscan.ScanOptions) (*gitscan.IncrementalScanResult, error) {
	if spec.SourceType != domain.RepositorySourceGitHub {
		return nil, fmt.Errorf("expected a GitHub scan, got %s", spec.SourceType)
	}
	spec.SourceType, spec.Source = domain.RepositorySourceLocalGit, s.path
	return s.scanner.ScanIncremental(ctx, spec, base, opts)
}

func searchRedisConfig(addr string) config.RedisConfig {
	return config.RedisConfig{
		Enabled: true, Addr: addr, KeyPrefix: "projectlens-test-" + uuid.NewString(),
		DialTimeout: 300 * time.Millisecond, ReadTimeout: 300 * time.Millisecond, WriteTimeout: 300 * time.Millisecond,
	}
}

func integrationSearchCache(t *testing.T) (*cache.RedisSearchCache, config.RedisConfig) {
	t.Helper()
	cfg := searchRedisConfig("")
	var client *cache.RedisSearchCache
	if rawURL := os.Getenv("TEST_REDIS_URL"); rawURL != "" {
		var err error
		client, err = cache.NewRedisSearchCacheFromURL(rawURL, cfg)
		if err != nil {
			t.Fatal("invalid TEST_REDIS_URL")
		}
		t.Cleanup(func() { _ = client.Close() })
	} else {
		cfg.Addr = miniredis.RunT(t).Addr()
		client = openSearchCache(t, cfg)
	}
	if err := client.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	return client, cfg
}

func openSearchCache(t *testing.T, cfg config.RedisConfig) *cache.RedisSearchCache {
	t.Helper()
	client := cache.NewRedisSearchCache(cfg)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func searchPath(projectID uuid.UUID, q string) string {
	return projects + "/" + projectID.String() + "/search?q=" + url.QueryEscape(q)
}

func searchRequest(t *testing.T, srv *httptest.Server, path string, status domain.SearchCacheStatus) domain.SearchResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result domain.SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Search-Cache") != string(status) ||
		result.CacheStatus != status || result.Items == nil || result.SearchVersion < 1 ||
		resp.Header.Get("X-Search-Version") != strconv.FormatInt(result.SearchVersion, 10) {
		t.Fatalf("%s = HTTP %d, headers %v, result %+v", path, resp.StatusCode, resp.Header, result)
	}
	return result
}

func TestUnifiedSearch(t *testing.T) {
	f := localSyncFixture(t)
	client, redisConfig := integrationSearchCache(t)
	f.cfg.Redis = redisConfig
	srv := startHTTP(f.cfg, f.pool, client)
	t.Cleanup(srv.Close)

	f.write(t, "src/quasar.txt", "Local path match only.\n")
	f.write(t, "src/copyX/file.txt", "Quasar neighboring file.\n")
	f.write(t, "src/A&b/nebula_notes.txt", "Another path match only.\n")
	localCommit := f.commit(t, "local search fixture")
	local := f.sync(t, "")
	localRepo := f.repo(t)

	remoteDir := t.TempDir()
	syncGit(t, remoteDir, "init", "--initial-branch=main", "--template=")
	remotePath := "src/a b#c.go"
	if err := os.Mkdir(filepath.Join(remoteDir, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteDir, remotePath), []byte("package main\n\n// Quasar is the remote term.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	syncGit(t, remoteDir, "add", "-A")
	syncGit(t, remoteDir, "commit", "--no-gpg-sign", "-m", "github search fixture")
	remoteCommit := syncGit(t, remoteDir, "rev-parse", "HEAD")
	owner, name, ghID := "octocat", "demo", int64(1)
	githubRepo, err := f.repos.Create(t.Context(), domain.Repository{
		ID: uuid.New(), ProjectID: f.projectID, SourceType: domain.RepositorySourceGitHub, Name: "GitHub Fixture",
		Source: "https://github.com/octocat/demo", CanonicalSource: "https://github.com/octocat/demo",
		RemoteURL: "https://github.com/octocat/demo", GitHubOwner: &owner, GitHubRepo: &name, GitHubRepoID: &ghID,
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteRoots := config.RepositoryConfig{AllowedLocalRoots: []string{remoteDir}}
	scanner := searchGitHubScanner{scanner: gitscan.NewScanner(f.cfg.Scanner, remoteRoots, f.cfg.GitHub), path: remoteDir}
	syncer := service.NewSyncService(f.repos, f.store, f.tx, scanner, f.cfg.Sync, f.log)
	remote, err := syncer.SyncRepository(t.Context(), f.projectID, githubRepo.ID, "", nil)
	if err != nil || remote.Status != domain.SyncOutcomePublished {
		t.Fatalf("publish GitHub snapshot = %+v, %v", remote, err)
	}

	base := projects + "/" + f.projectID.String()
	var doc domain.Document
	request(t, srv, "POST", base+"/documents", `{"title":"Quasar guide","content":"# Reference\nDetails for the reader."}`, 201, &doc)
	other := createDocumentProject(t, srv, "other-search-project")
	request(t, srv, "POST", projects+"/"+other.ID.String()+"/documents", `{"title":"Quasar elsewhere","content":"Isolated."}`, 201, nil)

	mainQuery := searchPath(f.projectID, "quasar")
	full := searchRequest(t, srv, mainQuery, domain.SearchCacheStatusMiss)
	if full.Total != 4 || len(full.Items) != 4 || full.Limit != 20 || full.Offset != 0 || full.Query != "quasar" {
		t.Fatalf("unified results = %+v", full)
	}
	published := map[domain.SearchSourceType]struct {
		repo   domain.Repository
		sync   domain.SyncResult
		commit string
	}{
		domain.SearchSourceLocalGit: {localRepo, local, localCommit},
		domain.SearchSourceGitHub:   {githubRepo, remote, remoteCommit},
	}
	for i, hit := range full.Items {
		if hit.ProjectID != f.projectID || hit.ID == uuid.Nil || hit.UpdatedAt.IsZero() || !strings.Contains(strings.ToLower(hit.Snippet), "<mark>quasar</mark>") {
			t.Fatalf("incomplete hit = %+v", hit)
		}
		if i > 0 && full.Items[i-1].Score < hit.Score || i < 2 && hit.Score <= full.Items[2].Score {
			t.Fatalf("unexpected ranking at %d: %+v", i, full.Items)
		}
		loc := hit.Location
		switch hit.SourceType {
		case domain.SearchSourceDocument:
			if hit.Kind != domain.SearchHitKindDocument || hit.ID != doc.ID || loc.DocumentID == nil || *loc.DocumentID != doc.ID ||
				loc.DocumentTitle != doc.Title || loc.DocumentVersion == nil || *loc.DocumentVersion != doc.Version ||
				loc.Line != nil || loc.APIPath != base+"/documents/"+doc.ID.String() || loc.RepositoryID != nil {
				t.Fatalf("document provenance = %+v", hit)
			}
			request(t, srv, "GET", loc.APIPath, "", 200, nil)
		case domain.SearchSourceLocalGit, domain.SearchSourceGitHub:
			want := published[hit.SourceType]
			if hit.Kind != domain.SearchHitKindSourceFile || loc.RepositoryID == nil || *loc.RepositoryID != want.repo.ID ||
				loc.RepositoryName != want.repo.Name || loc.RepositorySource != want.repo.Source || loc.SnapshotID == nil ||
				*loc.SnapshotID != want.sync.SnapshotID || loc.SnapshotVersion == nil || *loc.SnapshotVersion != want.sync.Version ||
				loc.CommitHash != want.commit || loc.Path != hit.Title ||
				loc.APIPath != base+"/repositories/"+want.repo.ID.String()+"/files/content?path="+url.QueryEscape(loc.Path) {
				t.Fatalf("source provenance = %+v", hit)
			}
			if hit.SourceType == domain.SearchSourceGitHub {
				wantURL := "https://github.com/octocat/demo/blob/" + remoteCommit + "/src/a%20b%23c.go#L3"
				if loc.Path != remotePath || loc.Line == nil || *loc.Line != 3 || loc.URL != wantURL ||
					!strings.Contains(hit.Snippet, "is the remote term") {
					t.Fatalf("GitHub content location = %+v", hit)
				}
			} else if loc.URL != "" || (loc.Path == "src/quasar.txt" &&
				(loc.Line != nil || hit.Snippet != "src/<mark>quasar</mark>.txt")) ||
				(loc.Path == "src/copyX/file.txt" && (loc.Line == nil || *loc.Line != 1)) {
				t.Fatalf("local content location = %+v", hit)
			}
		default:
			t.Fatalf("unknown source type in %+v", hit)
		}
	}
	hit := searchRequest(t, srv, mainQuery, domain.SearchCacheStatusHit)
	if !reflect.DeepEqual(full.Items, hit.Items) || full.SearchVersion != hit.SearchVersion {
		t.Fatalf("cache changed results: %+v", hit)
	}
	escapedPath := searchRequest(t, srv, searchPath(f.projectID, "nebula"), domain.SearchCacheStatusMiss)
	if escapedPath.Total != 1 || len(escapedPath.Items) != 1 || escapedPath.Items[0].Location.Line != nil ||
		escapedPath.Items[0].Snippet != "src/A&amp;b/<mark>nebula</mark>_notes.txt" {
		t.Fatalf("escaped path-only snippet = %+v", escapedPath)
	}
	for _, tc := range []struct {
		params string
		total  int64
		source domain.SearchSourceType
	}{
		{"&source_type=document", 1, domain.SearchSourceDocument},
		{"&source_type=github,document&source_type=github", 2, ""},
		{"&repository_id=" + githubRepo.ID.String(), 1, domain.SearchSourceGitHub},
		{"&repository_id=" + githubRepo.ID.String() + "&source_type=document", 0, ""},
		{"&path_prefix=src%2Fquasar", 1, domain.SearchSourceLocalGit},
		{"&path_prefix=src%2Fcopy_", 0, ""},
		{"&path_prefix=src%2Fcopy%25", 0, ""},
	} {
		page := searchRequest(t, srv, mainQuery+tc.params, domain.SearchCacheStatusMiss)
		if page.Total != tc.total || int64(len(page.Items)) != tc.total ||
			(tc.source != "" && page.Items[0].SourceType != tc.source) {
			t.Fatalf("filter %s = %+v", tc.params, page)
		}
	}
	page := searchRequest(t, srv, mainQuery+"&limit=1&offset=1", domain.SearchCacheStatusMiss)
	if page.Total != 4 || page.Limit != 1 || page.Offset != 1 || len(page.Items) != 1 || page.Items[0].ID != full.Items[1].ID {
		t.Fatalf("paginated results = %+v", page)
	}
	page = searchRequest(t, srv, mainQuery+"&offset=40", domain.SearchCacheStatusMiss)
	if page.Total != 4 || page.Offset != 40 || len(page.Items) != 0 {
		t.Fatalf("beyond last page = %+v", page)
	}
	page = searchRequest(t, srv, searchPath(f.projectID, "..."), domain.SearchCacheStatusMiss)
	if page.Total != 0 || len(page.Items) != 0 || page.SearchVersion != full.SearchVersion {
		t.Fatalf("punctuation-only query = %+v", page)
	}
	page = searchRequest(t, srv, searchPath(other.ID, "quasar"), domain.SearchCacheStatusMiss)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ProjectID != other.ID {
		t.Fatalf("project isolation = %+v", page)
	}
	request(t, srv, "GET", searchPath(uuid.New(), "quasar"), "", 404, nil)

	assertSearchIndexes(t, f, local.SnapshotID)

	request(t, srv, "DELETE", base, "", 204, nil)
	request(t, srv, "GET", mainQuery, "", 404, nil)
	var childrenRemain bool
	err = f.pool.QueryRow(t.Context(), `SELECT
        EXISTS (SELECT 1 FROM documents WHERE project_id = $1) OR
        EXISTS (SELECT 1 FROM repositories WHERE project_id = $1) OR
        EXISTS (SELECT 1 FROM snapshots WHERE repository_id IN ($2, $3)) OR
        EXISTS (SELECT 1 FROM source_files WHERE repository_id IN ($2, $3))`,
		f.projectID, localRepo.ID, githubRepo.ID).Scan(&childrenRemain)
	if err != nil || childrenRemain {
		t.Fatalf("project cascade left searchable children: remaining=%t, error=%v", childrenRemain, err)
	}
}

func TestUnifiedSearchInvalidation(t *testing.T) {
	f := localSyncFixture(t)
	client, redisConfig := integrationSearchCache(t)
	f.cfg.Redis = redisConfig
	srv := startHTTP(f.cfg, f.pool, client)
	t.Cleanup(srv.Close)
	store := repository.NewPostgresSearchStore(f.pool)
	version := func() int64 {
		t.Helper()
		value, err := store.GetProjectSearchVersion(t.Context(), f.projectID)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	initial := version()
	if initial != 1 {
		t.Fatalf("initial search version = %d", initial)
	}

	docBase := projects + "/" + f.projectID.String() + "/documents"
	var doc domain.Document
	request(t, srv, "POST", docBase, `{"title":"Guide","content":"# Intro\noldnote"}`, 201, &doc)
	docPath := docBase + "/" + doc.ID.String()
	previous := searchRequest(t, srv, searchPath(f.projectID, "oldnote"), domain.SearchCacheStatusMiss)
	if previous.SearchVersion != initial+1 || previous.Total != 1 || len(previous.Items) != 1 ||
		previous.Items[0].Location.Line == nil || *previous.Items[0].Location.Line != 2 {
		t.Fatalf("created document results = %+v", previous)
	}
	searchRequest(t, srv, searchPath(f.projectID, "oldnote"), domain.SearchCacheStatusHit)

	abort := errors.New("abort search version mutation")
	err := f.tx.WithinTx(t.Context(), func(ctx context.Context) error {
		_, err := db.ConnFromContext(ctx, f.pool).Exec(ctx, "UPDATE documents SET content = 'rolledback' WHERE id = $1", doc.ID)
		if err != nil {
			return err
		}
		observed, err := store.GetProjectSearchVersion(ctx, f.projectID)
		if err != nil || observed != previous.SearchVersion+1 {
			t.Fatalf("document transaction version = %d, %v", observed, err)
		}
		return abort
	})
	if !errors.Is(err, abort) || version() != previous.SearchVersion {
		t.Fatalf("document version survived rollback: %v", err)
	}
	searchRequest(t, srv, searchPath(f.projectID, "oldnote"), domain.SearchCacheStatusHit)

	expectDocumentResponse(t, srv, "PUT", docPath, `{"title":"Guide","content":"revisednote"}`, `"1"`, 200)
	for _, tc := range []struct {
		gone, fresh string
		docVersion  int64
	}{
		{"oldnote", "revisednote", 2},
		{"revisednote", "patchednote", 3},
		{"patchednote", "", 4},
	} {
		switch tc.docVersion {
		case 3:
			expectDocumentResponse(t, srv, "PATCH", docPath, `{"content":"patchednote"}`, `"2"`, 200)
		case 4:
			expectDocumentResponse(t, srv, "DELETE", docPath, "", `"3"`, 204)
		}
		result := searchRequest(t, srv, searchPath(f.projectID, tc.gone), domain.SearchCacheStatusMiss)
		if result.SearchVersion != previous.SearchVersion+1 || result.Total != 0 {
			t.Fatalf("stale document result after version %d: %+v", tc.docVersion, result)
		}
		previous = result
		if tc.fresh != "" {
			path := searchPath(f.projectID, tc.fresh)
			result = searchRequest(t, srv, path, domain.SearchCacheStatusMiss)
			if result.Total != 1 || len(result.Items) != 1 || result.Items[0].Location.DocumentVersion == nil ||
				*result.Items[0].Location.DocumentVersion != tc.docVersion {
				t.Fatalf("fresh document result: %+v", result)
			}
			searchRequest(t, srv, path, domain.SearchCacheStatusHit)
		}
	}

	f.write(t, "src/modified.txt", "oldcode\n")
	f.write(t, "src/deleted.txt", "deletedcode\n")
	f.commit(t, "initial source search content")
	first := f.sync(t, "")
	if version() <= previous.SearchVersion {
		t.Fatal("active snapshot publish did not bump version")
	}
	for _, term := range []string{"oldcode", "deletedcode"} {
		path := searchPath(f.projectID, term)
		if result := searchRequest(t, srv, path, domain.SearchCacheStatusMiss); result.Total != 1 {
			t.Fatalf("initial source result = %+v", result)
		}
		searchRequest(t, srv, path, domain.SearchCacheStatusHit)
	}
	beforeInactive := version()
	for i, status := range []domain.SnapshotStatus{domain.SnapshotStatusStaging, domain.SnapshotStatusFailed} {
		insertInactiveSearchSnapshot(t, f, first.SnapshotID, int64(i+2), status)
	}
	if version() != beforeInactive {
		t.Fatal("inactive snapshots changed search version")
	}
	if result := searchRequest(t, srv, searchPath(f.projectID, "inactivecode"), domain.SearchCacheStatusMiss); result.Total != 0 {
		t.Fatalf("inactive files appeared in search: %+v", result)
	}

	f.write(t, "src/modified.txt", "freshcode\n")
	if err := os.Remove(filepath.Join(f.dir, "src/deleted.txt")); err != nil {
		t.Fatal(err)
	}
	commit := f.commit(t, "modify and delete source files")
	second := f.sync(t, "")
	if second.Status != domain.SyncOutcomePublished || second.ModifiedFiles != 1 || second.DeletedFiles != 1 || version() <= beforeInactive {
		t.Fatalf("second snapshot: %+v, version = %d", second, version())
	}
	for _, term := range []string{"oldcode", "deletedcode"} {
		if result := searchRequest(t, srv, searchPath(f.projectID, term), domain.SearchCacheStatusMiss); result.Total != 0 {
			t.Fatalf("superseded content remained searchable: %+v", result)
		}
	}
	fresh := searchPath(f.projectID, "freshcode")
	result := searchRequest(t, srv, fresh, domain.SearchCacheStatusMiss)
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].Location.CommitHash != commit {
		t.Fatalf("fresh source result = %+v", result)
	}
	if unchanged := f.sync(t, ""); unchanged.Status != domain.SyncOutcomeUnchanged {
		t.Fatalf("unchanged sync = %+v", unchanged)
	}
	if next := searchRequest(t, srv, fresh, domain.SearchCacheStatusHit); next.SearchVersion != result.SearchVersion {
		t.Fatalf("unchanged sync invalidated cached result: %+v", next)
	}

	renamed := "Renamed repository"
	if _, err := f.repos.Patch(t.Context(), f.projectID, f.repoID, domain.PatchRepositoryInput{Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	afterRename := searchRequest(t, srv, fresh, domain.SearchCacheStatusMiss)
	if afterRename.SearchVersion != result.SearchVersion+1 || len(afterRename.Items) != 1 ||
		afterRename.Items[0].Location.RepositoryName != renamed {
		t.Fatalf("renamed repository provenance = %+v", afterRename)
	}
	if err := f.repos.Delete(t.Context(), f.projectID, f.repoID); err != nil {
		t.Fatal(err)
	}
	afterDelete := searchRequest(t, srv, fresh, domain.SearchCacheStatusMiss)
	if afterDelete.SearchVersion <= afterRename.SearchVersion || afterDelete.Total != 0 {
		t.Fatalf("deleted repository result = %+v", afterDelete)
	}
}

func insertInactiveSearchSnapshot(t *testing.T, f *syncFixture, active uuid.UUID, version int64, status domain.SnapshotStatus) {
	t.Helper()
	id := uuid.New()
	_, err := f.pool.Exec(t.Context(), `INSERT INTO snapshots (
 id, repository_id, version, status, resolved_ref, commit_hash, tree_hash, scanner_config_hash,
 commit_author_name, commit_author_email, commit_authored_at, commit_committer_name, commit_committer_email,
 commit_committed_at, commit_subject, total_tree_entries, included_files, included_bytes, skipped_files,
 added_files, modified_files, deleted_files, unchanged_files, blobs_read, scanned_at)
 SELECT $1, repository_id, $2, $3, resolved_ref, commit_hash, tree_hash, scanner_config_hash,
 commit_author_name, commit_author_email, commit_authored_at, commit_committer_name, commit_committer_email,
 commit_committed_at, commit_subject, 1, 1, 12, 0, 1, 0, 0, 0, 1, scanned_at
 FROM snapshots WHERE id = $4`, id, version, status, active)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(t.Context(), `INSERT INTO source_files
 (id, snapshot_id, repository_id, path, blob_oid, mode, size_bytes, sha256, line_count, content)
 SELECT $1, $2, repository_id, 'inactive.txt', commit_hash, '100644', 12, repeat('a', 64), 1, 'inactivecode'
 FROM snapshots WHERE id = $2`, uuid.New(), id)
	if err != nil {
		t.Fatal(err)
	}
}

func TestUnifiedSearchRedisOutage(t *testing.T) {
	f := newSyncFixture(t)
	redisServer := miniredis.RunT(t)
	f.cfg.Redis = searchRedisConfig(redisServer.Addr())
	client := openSearchCache(t, f.cfg.Redis)
	srv := startHTTP(f.cfg, f.pool, client)
	t.Cleanup(srv.Close)
	request(t, srv, "POST", projects+"/"+f.projectID.String()+"/documents",
		`{"title":"Searchable while offline","content":"Resilient."}`, 201, nil)
	path := searchPath(f.projectID, "resilient")
	searchRequest(t, srv, path, domain.SearchCacheStatusMiss)
	searchRequest(t, srv, path, domain.SearchCacheStatusHit)

	redisServer.Close()
	offline := searchRequest(t, srv, path, domain.SearchCacheStatusDegraded)
	if offline.Total != 1 {
		t.Fatalf("search during Redis outage = %+v", offline)
	}
	request(t, srv, "GET", "/readyz", "", 200, nil)

	// Use the stopped Redis listener's address to test serving with an unreachable cache at startup.
	startupCache := openSearchCache(t, f.cfg.Redis)
	if err := startupCache.Ping(t.Context()); err == nil {
		t.Fatal("startup probe unexpectedly reached Redis")
	}
	offlineServer := startHTTP(f.cfg, f.pool, startupCache)
	t.Cleanup(offlineServer.Close)
	if result := searchRequest(t, offlineServer, path, domain.SearchCacheStatusDegraded); result.Total != 1 {
		t.Fatalf("search after unavailable startup = %+v", result)
	}
	request(t, offlineServer, "GET", "/readyz", "", 200, nil)

	if err := redisServer.Restart(); err != nil {
		t.Fatal(err)
	}
	if result := searchRequest(t, offlineServer, path, domain.SearchCacheStatusHit); !reflect.DeepEqual(result.Items, offline.Items) {
		t.Fatalf("search after Redis recovery = %+v", result)
	}
}

type searchSQLTracer struct {
	query pgx.TraceQueryStartData
}

func (tracer *searchSQLTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(strings.TrimSpace(query.SQL), "WITH q AS ") {
		tracer.query = query
	}
	return ctx
}

func (tracer *searchSQLTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func assertSearchIndexes(t *testing.T, f *syncFixture, snapshotID uuid.UUID) {
	t.Helper()
	ctx := t.Context()

	// Give the planner enough same-project distractors to prefer GIN over project/snapshot index scans.
	_, err := f.pool.Exec(ctx, `INSERT INTO documents (id, project_id, title, content)
 SELECT gen_random_uuid(), $1, 'Background ' || n, 'unrelated' FROM generate_series(1, 1500) n`, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(ctx, `INSERT INTO source_files
 (id, snapshot_id, repository_id, path, blob_oid, mode, size_bytes, sha256, line_count, content)
 SELECT gen_random_uuid(), s.id, s.repository_id, 'background/' || n || '.txt',
        s.commit_hash, '100644', 9, repeat('a', 64), 1, 'unrelated'
 FROM snapshots s CROSS JOIN generate_series(1, 1500) n WHERE s.id = $1`, snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"VACUUM (ANALYZE) source_files", "VACUUM (ANALYZE) documents"} {
		if _, err := f.pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}

	tracer := &searchSQLTracer{}
	cfg := f.pool.Config()
	cfg.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := repository.NewPostgresSearchStore(pool)
	err = db.NewPgxTxManager(pool).WithinTx(ctx, func(ctx context.Context) error {
		conn := db.ConnFromContext(ctx, pool)
		if _, err := conn.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
			return err
		}
		page, err := store.Search(ctx, f.projectID, domain.SearchFilter{Query: "quasar", Limit: 10})
		if err != nil {
			return err
		}
		if page.Total != 4 || tracer.query.SQL == "" {
			return fmt.Errorf("unexpected search result or missing SQL trace: %+v", page)
		}
		var plan []byte
		if err := conn.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+tracer.query.SQL, tracer.query.Args...).Scan(&plan); err != nil {
			return err
		}
		var nodes []struct {
			Plan explainSearchNode
		}
		if err := json.Unmarshal(plan, &nodes); err != nil {
			return err
		}
		for _, index := range []string{"idx_source_files_search_vector", "idx_documents_search_vector"} {
			if len(nodes) != 1 || !nodes[0].Plan.usesIndex(index) {
				return fmt.Errorf("query did not use %s via a Bitmap Index Scan: %s", index, plan)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type explainSearchNode struct {
	Type  string `json:"Node Type"`
	Index string `json:"Index Name"`
	Plans []explainSearchNode
}

func (n explainSearchNode) usesIndex(index string) bool {
	if n.Type == "Bitmap Index Scan" && n.Index == index {
		return true
	}
	for _, child := range n.Plans {
		if child.usesIndex(index) {
			return true
		}
	}
	return false
}
