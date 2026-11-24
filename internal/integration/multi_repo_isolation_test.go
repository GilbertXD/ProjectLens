package integration

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

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

const isolationRemoteURL = "https://github.com/owner/software"

func isolationGitFixtures(t *testing.T, f *syncFixture) (local, reference, commonSHA, localSHA, mirrorSHA, referenceSHA string) {
	t.Helper()
	root := t.TempDir()
	f.cfg.Repository.AllowedLocalRoots = []string{root}
	local, reference = filepath.Join(root, "software"), filepath.Join(root, "reference")
	mirrorWork := filepath.Join(t.TempDir(), "mirror")
	for _, dir := range []string{local, reference} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		syncGit(t, dir, "init", "--initial-branch=main", "--template=")
	}
	f.dir = local
	f.write(t, "shared.txt", "Shared history\n")
	commonSHA = f.commit(t, "shared base")
	syncGit(t, local, "clone", "--", local, mirrorWork)
	syncGit(t, local, "remote", "add", "origin", isolationRemoteURL)
	f.write(t, "src/local.txt", "isolationmarker V1\n")
	localSHA = f.commit(t, "local V1")

	f.dir = mirrorWork
	f.write(t, "src/mirror.txt", "isolationmarker V2\n")
	mirrorSHA = f.commit(t, "mirror V2")
	web := t.TempDir()
	bare := filepath.Join(web, "owner", "software.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0700); err != nil {
		t.Fatal(err)
	}
	syncGit(t, mirrorWork, "clone", "--bare", "--", mirrorWork, bare)
	syncGit(t, bare, "update-server-info")
	mirror := httptest.NewServer(http.FileServer(http.Dir(web)))
	t.Cleanup(mirror.Close)
	f.cfg.GitHub.GitBaseURL = mirror.URL

	f.dir = reference
	f.write(t, "src/reference.txt", "isolationmarker reference\n")
	referenceSHA = f.commit(t, "reference")
	return
}

func isolationGitHub(t *testing.T, sha string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const root = "/repos/owner/software"
		const dates = `"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"`
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case root:
			fmt.Fprint(w, `{"id":42,"full_name":"owner/software","default_branch":"main","html_url":"`+isolationRemoteURL+`"}`)
		case root + "/issues":
			fmt.Fprint(w, `[{"id":101,"number":1,"title":"Mirror issue","html_url":"`+isolationRemoteURL+`/issues/1",`+dates+`},`+
				`{"id":102,"number":2,"pull_request":{},"html_url":"`+isolationRemoteURL+`/pull/2",`+dates+`}]`)
		case root + "/pulls/2":
			fmt.Fprint(w, `{"id":201,"number":2,"title":"Mirror pull","commits":1,"html_url":"`+isolationRemoteURL+`/pull/2",`+dates+`}`)
		case root + "/issues/comments":
			fmt.Fprint(w, `[{"id":301,"issue_url":"https://api.github.com`+root+`/issues/1","body":"Mirror issue comment",`+
				`"html_url":"`+isolationRemoteURL+`/issues/1#issuecomment-301",`+dates+`}]`)
		case root + "/pulls/comments":
			fmt.Fprint(w, `[{"id":401,"pull_request_url":"https://api.github.com`+root+`/pulls/2","body":"Mirror review comment",`+
				`"html_url":"`+isolationRemoteURL+`/pull/2#discussion_r401",`+dates+`}]`)
		case root + "/pulls/2/commits":
			fmt.Fprint(w, `[{"sha":"`+sha+`","html_url":"`+isolationRemoteURL+`/commit/`+sha+`"}]`)
		default:
			t.Errorf("unexpected GitHub fixture request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type isolationRepository struct {
	domain.Repository
	path, sha, file, content string
	note                     domain.Document
	snapshot                 domain.SyncResult
}

func TestMultiRepositoryIsolation(t *testing.T) {
	f := newSyncFixture(t)
	ctx := t.Context()
	local, reference, commonSHA, localSHA, mirrorSHA, referenceSHA := isolationGitFixtures(t, f)
	upstream := isolationGitHub(t, mirrorSHA)
	f.cfg.GitHub.APIBaseURL = upstream.URL
	f.cfg.GitHub.Token = ""
	client := github.NewClient(f.cfg.GitHub, upstream.Client())
	f.scanner = gitscan.NewScanner(f.cfg.Scanner, f.cfg.Repository, f.cfg.GitHub)
	f.svc = service.NewSyncService(f.repos, f.store, f.tx, f.scanner, f.cfg.Sync, f.log)
	history := &service.HistoryService{Repositories: f.repos, Locks: f.store, Scanner: f.scanner, Tx: f.tx}
	activity := &service.ActivityService{Repositories: f.repos, Locks: f.store, Client: client, Tx: f.tx}
	projectsStore := repository.NewPostgresProjectRepository(f.pool)
	repos := service.NewRepositoryService(projectsStore, f.repos, gitrepo.NewLocalInspector(f.cfg.Repository.AllowedLocalRoots), client)
	docs := service.NewDocumentService(projectsStore, repository.NewPostgresDocumentStore(f.pool), f.cfg.Document.MaxContentBytes)
	searchStore := repository.NewPostgresSearchStore(f.pool)
	cache, redisConfig := integrationSearchCache(t)
	search := service.NewSearchService(searchStore, cache, f.cfg.Search, redisConfig.KeyPrefix, f.log)
	registry := &worker.Registry{}
	if err := worker.RegisterSyncHandler(registry, f.svc); err != nil {
		t.Fatal(err)
	}
	jobsStore := repository.NewPostgresJobStore(f.pool)
	jobs := service.NewJobService(jobsStore, registry, f.cfg.Worker)
	schedule := &service.Scheduler{Repositories: f.repos, Tx: f.tx, Jobs: jobs}
	api := httptest.NewServer(router.New(f.cfg, f.log, router.Dependencies{
		Projects:     handler.NewProjectHandler(service.NewProjectService(projectsStore, f.tx), f.log),
		Repositories: handler.NewRepositoryHandler(repos, f.log),
		Snapshots:    handler.NewSnapshotHandler(f.svc, f.log),
		History:      &handler.HistoryHandler{Service: history, Log: f.log},
		Activity:     &handler.ActivityHandler{Store: f.repos, Log: f.log},
		Documents:    handler.NewDocumentHandler(docs, f.log, f.cfg.Document.MaxContentBytes),
		Search:       handler.NewSearchHandler(search, f.log),
		Schedule:     &handler.ScheduleHandler{Service: schedule, Log: f.log},
		Jobs:         handler.NewJobHandler(jobs, f.log),
	}))
	t.Cleanup(api.Close)
	base := projects + "/" + f.projectID.String()
	otherProject := createDocumentProject(t, api, "other-isolation-project")
	otherBase := projects + "/" + otherProject.ID.String()

	attach := func(projectPath, source string, kind domain.RepositorySourceType, role, name string) isolationRepository {
		t.Helper()
		var r isolationRepository
		request(t, api, "POST", projectPath+"/repositories",
			fmt.Sprintf(`{"source_type":%q,"source":%q,"role":%q,"name":%q}`, kind, source, role, name), 201, &r.Repository)
		r.path = projectPath + "/repositories/" + r.ID.String()
		if string(r.Role) != role {
			t.Fatalf("associated repository has role %q, want %q", r.Role, role)
		}
		return r
	}
	a := attach(base, local, domain.RepositorySourceLocalGit, "primary", "software")
	b := attach(base, isolationRemoteURL, domain.RepositorySourceGitHub, "primary", "software")
	c := attach(base, reference, domain.RepositorySourceLocalGit, "reference", "reference")
	foreign := attach(otherBase, local, domain.RepositorySourceLocalGit, "primary", "software")
	if a.ID == b.ID || a.Name != b.Name || a.RemoteURL != isolationRemoteURL || a.RemoteURL != b.RemoteURL {
		t.Fatalf("local and GitHub association metadata = %+v, %+v", a.Repository, b.Repository)
	}
	for _, role := range []domain.RepositoryRole{domain.RepositoryRolePrimary, domain.RepositoryRoleReference} {
		var list domain.RepositoryListResult
		request(t, api, "GET", base+"/repositories?role="+string(role), "", 200, &list)
		want := []uuid.UUID{a.ID, b.ID}
		if role == domain.RepositoryRoleReference {
			want = []uuid.UUID{c.ID}
		}
		var got []uuid.UUID
		for _, item := range list.Items {
			if item.Role != role {
				t.Fatalf("role filter returned %+v", item)
			}
			got = append(got, item.ID)
		}
		if list.Total != int64(len(want)) || len(got) != len(want) ||
			slices.ContainsFunc(want, func(id uuid.UUID) bool { return !slices.Contains(got, id) }) {
			t.Fatalf("repositories with role %q = %v, want %v", role, got, want)
		}
	}

	a.sha, a.file, a.content = localSHA, "src/local.txt", "isolationmarker V1\n"
	b.sha, b.file, b.content = mirrorSHA, "src/mirror.txt", "isolationmarker V2\n"
	c.sha, c.file, c.content = referenceSHA, "src/reference.txt", "isolationmarker reference\n"
	for _, repo := range []*isolationRepository{&a, &b, &c} {
		snapshot, err := f.svc.SyncRepository(ctx, f.projectID, repo.ID, "main", nil)
		if err != nil || snapshot.Status != domain.SyncOutcomePublished || snapshot.CommitHash != repo.sha || snapshot.Version != 1 {
			t.Fatalf("publish %s: %+v %v", repo.path, snapshot, err)
		}
		repo.snapshot = snapshot
		page, err := history.Sync(ctx, domain.SyncTarget{ProjectID: f.projectID, RepositoryID: repo.ID, Ref: "main"})
		if err != nil || page.Tip != repo.sha {
			t.Fatalf("import history %s: %+v %v", repo.path, page, err)
		}
		request(t, api, "PUT", repo.path+"/sync-policy/files", `{"enabled":true,"interval_seconds":90}`, 200, nil)
	}
	if n, err := activity.Sync(ctx, domain.SyncTarget{ProjectID: f.projectID, RepositoryID: b.ID}); err != nil || n != 5 {
		t.Fatalf("sync GitHub activity = %d %v", n, err)
	}
	if _, err := activity.Sync(ctx, domain.SyncTarget{ProjectID: f.projectID, RepositoryID: a.ID}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("local activity sync = %v", err)
	}

	for _, pair := range [][2]isolationRepository{{a, b}, {b, a}, {c, b}} {
		assertIsolatedRepository(t, api, pair[0], pair[1], commonSHA)
	}
	assertIsolatedActivity(t, api, b, a, c)

	request(t, api, "POST", a.path+"/documents", `{"title":"Local isolationmarker note"}`, 201, &a.note)
	request(t, api, "POST", base+"/documents", fmt.Sprintf(`{"title":"Mirror isolationmarker note","repository_id":%q}`, b.ID), 201, &b.note)
	request(t, api, "POST", c.path+"/documents", `{"title":"Reference isolationmarker note"}`, 201, &c.note)
	request(t, api, "POST", foreign.path+"/documents", `{"title":"Foreign isolationmarker note"}`, 201, &foreign.note)
	var projectDoc domain.Document
	request(t, api, "POST", base+"/documents", `{"title":"Project isolationmarker note"}`, 201, &projectDoc)
	if projectDoc.Scope != domain.KnowledgeScopeProject || projectDoc.RepositoryID != nil {
		t.Fatalf("project document = %+v", projectDoc)
	}
	assertRepositoryDocuments(t, api, base, []domain.Document{projectDoc}, a, b, c)
	assertForeignRepositoryRequests(t, api, base, a, foreign)
	assertRepositoryDocumentWrites(t, api, a)
	if _, err := searchStore.Search(ctx, f.projectID, domain.SearchFilter{Query: "isolationmarker", Limit: 20, RepositoryIDs: []uuid.UUID{a.ID, foreign.ID}}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("store accepted a foreign repository in search: %v", err)
	}
	_, err := f.pool.Exec(ctx, "INSERT INTO documents (id, project_id, repository_id, title) VALUES ($1, $2, $3, 'Foreign note')", uuid.New(), f.projectID, foreign.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "documents_repository_scope_fkey" {
		t.Fatalf("composite foreign key did not reject a foreign repository: %v", err)
	}

	projectSearch := base + "/search?q=isolationmarker"
	for _, tc := range []struct {
		path       string
		repos      []isolationRepository
		projectDoc bool
		filesOnly  bool
	}{
		{a.path + "/search?q=isolationmarker", []isolationRepository{a}, false, false},
		{projectSearch + "&repository_id=" + b.ID.String(), []isolationRepository{b}, false, false},
		{projectSearch + "&scope=repository&repository_id=" + c.ID.String(), []isolationRepository{c}, false, false},
		{projectSearch + "&scope=project", nil, true, false},
		{projectSearch + "&scope=cross_repository", []isolationRepository{a, b, c}, false, false},
		{projectSearch + "&repository_ids=" + b.ID.String() + "," + c.ID.String(), []isolationRepository{b, c}, false, false},
		{projectSearch + "&repository_role=reference&include_project_documents=true", []isolationRepository{c}, true, false},
		{projectSearch + "&scope=cross_repository&repository_role=primary&repository_ids=" + b.ID.String() + "," + c.ID.String(), []isolationRepository{b}, false, false},
		{projectSearch + "&scope=cross_repository&include_project_documents=true&path_prefix=src/", []isolationRepository{a, b, c}, false, true},
		{projectSearch + "&scope=all", []isolationRepository{a, b, c}, true, false},
	} {
		result := searchRequest(t, api, tc.path, domain.SearchCacheStatusMiss)
		var projectNote domain.Document
		if tc.projectDoc {
			projectNote = projectDoc
		}
		assertIsolationSearch(t, result, base, projectDocuments(projectNote), tc.filesOnly, tc.repos...)
	}
	// The scope must be explicit: no implicit widening and no silent correction.
	for _, path := range []string{
		projectSearch,
		projectSearch + "&scope=all&repository_role=primary",
		projectSearch + "&scope=all&repository_id=" + a.ID.String(),
		projectSearch + "&scope=all&include_project_documents=true",
		projectSearch + "&scope=project&repository_role=primary",
		projectSearch + "&scope=project&include_project_documents=true",
		projectSearch + "&scope=repository",
		projectSearch + "&scope=cross_repository&repository_id=" + a.ID.String(),
		projectSearch + "&scope=unknown",
		a.path + "/search?q=isolationmarker&scope=cross_repository",
		a.path + "/search?q=isolationmarker&repository_ids=" + b.ID.String(),
		a.path + "/search?q=isolationmarker&repository_role=primary",
		a.path + "/search?q=isolationmarker&include_project_documents=true",
		b.path + "/search?q=isolationmarker&repository_id=" + a.ID.String(),
	} {
		request(t, api, "GET", path, "", 400, nil)
	}
	// Scopes own separate cache entries, and an equivalent shorthand reuses one.
	if result := searchRequest(t, api, projectSearch+"&scope=repository&repository_id="+b.ID.String(), domain.SearchCacheStatusHit); len(result.Items) != 1 {
		t.Fatalf("equivalent repository shorthand missed the cache: %+v", result)
	}
	searchRequest(t, api, projectSearch+"&scope=project", domain.SearchCacheStatusHit)
	searchRequest(t, api, projectSearch+"&scope=all", domain.SearchCacheStatusHit)
	filtered := projectSearch + "&repository_role=primary"
	primaries := searchRequest(t, api, filtered, domain.SearchCacheStatusMiss)
	assertIsolationSearch(t, primaries, base, nil, false, a, b)
	searchRequest(t, api, filtered, domain.SearchCacheStatusHit)
	request(t, api, "PATCH", a.path, `{"role":"reference"}`, 200, &a.Repository)
	if a.Role != domain.RepositoryRoleReference {
		t.Fatalf("role patch returned %+v", a.Repository)
	}
	changed := searchRequest(t, api, filtered, domain.SearchCacheStatusMiss)
	if changed.SearchVersion <= primaries.SearchVersion {
		t.Fatalf("role change did not advance search version: %d to %d", primaries.SearchVersion, changed.SearchVersion)
	}
	assertIsolationSearch(t, changed, base, nil, false, b)
	assertIsolationSearch(t, searchRequest(t, api, a.path+"/search?q=isolationmarker", domain.SearchCacheStatusMiss), base, nil, false, a)

	var pendingA, pendingB struct {
		JobID uuid.UUID `json:"job_id"`
	}
	request(t, api, "POST", a.path+"/sync", "", 202, &pendingA)
	request(t, api, "POST", b.path+"/sync", "", 202, &pendingB)
	running := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO jobs (id, type, status, payload, locked_by, lease_token, lease_until)
		VALUES ($1, 'repository.commits.sync', 'running', $2, 'test-worker', $3, now() + interval '1 minute')`,
		running, map[string]string{"project_id": f.projectID.String(), "repository_id": strings.ToUpper(a.ID.String())}, uuid.New()); err != nil {
		t.Fatal(err)
	}
	before := searchRequest(t, api, projectSearch, domain.SearchCacheStatusMiss)
	searchRequest(t, api, projectSearch, domain.SearchCacheStatusHit)
	request(t, api, "DELETE", a.path, "", 204, nil)
	preserved := disassociatedDocument(a.note, a)
	after := searchRequest(t, api, projectSearch, domain.SearchCacheStatusMiss)
	if after.SearchVersion <= before.SearchVersion {
		t.Fatalf("disassociation did not advance search version: %d to %d", before.SearchVersion, after.SearchVersion)
	}
	assertIsolationSearch(t, after, base, []domain.Document{projectDoc, preserved}, false, b, c)
	assertDisassociatedData(t, f, a.ID)
	job, err := jobsStore.GetByID(ctx, pendingA.JobID)
	if err != nil || job.Status != domain.JobCancelled || job.FinishedAt == nil || job.LastErrorCode == nil ||
		*job.LastErrorCode != "cancelled" || job.LastError == nil || *job.LastError != "repository disassociated" {
		t.Fatalf("disassociated pending job = %+v %v", job, err)
	}
	job, err = jobsStore.GetByID(ctx, running)
	if err != nil || job.Status != domain.JobRunning || job.CancelRequestedAt == nil || job.FinishedAt != nil {
		t.Fatalf("disassociated running job = %+v %v", job, err)
	}
	job, err = jobsStore.GetByID(ctx, pendingB.JobID)
	if err != nil || job.Status != domain.JobPending || job.CancelRequestedAt != nil {
		t.Fatalf("surviving repository job = %+v %v", job, err)
	}
	request(t, api, "GET", a.path+"/search?q=isolationmarker", "", 404, nil)
	var kept domain.Document
	request(t, api, "GET", base+"/documents/"+a.note.ID.String(), "", 200, &kept)
	if kept.Scope != domain.KnowledgeScopeProject || kept.RepositoryID != nil || !sameOrigin(kept.Origin, preserved.Origin) ||
		kept.Title != preserved.Title || kept.Version != preserved.Version {
		t.Fatalf("preserved document = %+v, want %+v", kept, preserved)
	}
	request(t, api, "PATCH", base+"/documents/"+a.note.ID.String(),
		fmt.Sprintf(`{"title":"Stale","version":%d}`, a.note.Version), 409, nil)
	assertRepositoryDocuments(t, api, base, []domain.Document{projectDoc, preserved}, b, c)
	for _, repo := range []isolationRepository{b, c} {
		assertIsolatedRepository(t, api, repo, a, commonSHA)
		var policies struct{ Items []domain.Policy }
		request(t, api, "GET", repo.path+"/sync-policy", "", 200, &policies)
		if len(policies.Items) != 1 || !policies.Items[0].Enabled || policies.Items[0].IntervalSeconds != 90 {
			t.Fatalf("surviving policy = %+v", policies)
		}
	}
	assertIsolatedActivity(t, api, b, c)
	var cursorExists bool
	if err := f.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM github_activity_state WHERE repository_id = $1)", b.ID).Scan(&cursorExists); err != nil || !cursorExists {
		t.Fatalf("surviving GitHub activity state = %t, %v", cursorExists, err)
	}
	request(t, api, "DELETE", b.path, "", 204, nil)
	assertDisassociatedData(t, f, b.ID)
	assertRepositoryDocuments(t, api, base, []domain.Document{projectDoc, preserved, disassociatedDocument(b.note, b)}, c)
}

// disassociatedDocument is the document a repository-owned note becomes once its
// repository is disassociated: it survives at project level, keeps the
// repository as read-only origin, and increments its version so ETags issued
// against the repository scope go stale.
func disassociatedDocument(note domain.Document, repo isolationRepository) domain.Document {
	note.Scope, note.RepositoryID = domain.KnowledgeScopeProject, nil
	note.Origin = &domain.DocumentOrigin{RepositoryID: repo.ID, RepositoryName: repo.Name}
	note.Version++
	return note
}

func assertIsolatedRepository(t *testing.T, api *httptest.Server, repo, neighbor isolationRepository, commonSHA string) {
	t.Helper()
	var active domain.Snapshot
	request(t, api, "GET", repo.path+"/snapshots/active", "", 200, &active)
	var snapshots domain.SnapshotListResult
	request(t, api, "GET", repo.path+"/snapshots", "", 200, &snapshots)
	if active.ID != repo.snapshot.SnapshotID || active.CommitHash != repo.sha || active.RepositoryID != repo.ID ||
		snapshots.Total != 1 || len(snapshots.Items) != 1 || snapshots.Items[0].ID != active.ID {
		t.Fatalf("snapshot provenance for %s = %+v, %+v", repo.path, active, snapshots)
	}
	var files domain.SourceFileListResult
	request(t, api, "GET", repo.path+"/files?path_prefix=src/", "", 200, &files)
	if files.Total != 1 || len(files.Items) != 1 || files.Items[0].RepositoryID != repo.ID ||
		files.Items[0].SnapshotID != active.ID || files.Items[0].Path != repo.file {
		t.Fatalf("files for %s = %+v", repo.path, files)
	}
	var file domain.SourceFile
	request(t, api, "GET", repo.path+"/files/content?path="+repo.file, "", 200, &file)
	if file.ID != files.Items[0].ID || file.Content == nil || *file.Content != repo.content {
		t.Fatalf("file content for %s = %+v", repo.path, file)
	}
	request(t, api, "GET", repo.path+"/files/content?path="+neighbor.file, "", 404, nil)
	var commits struct {
		Tip   string
		Items []gitscan.HistoryCommit
	}
	request(t, api, "GET", repo.path+"/commits?ref=main", "", 200, &commits)
	got := make([]string, 0, len(commits.Items))
	for _, commit := range commits.Items {
		got = append(got, commit.Hash)
	}
	want := []string{repo.sha}
	if repo.Name == "software" {
		want = append(want, commonSHA)
	}
	if commits.Tip != repo.sha || !slices.Equal(got, want) {
		t.Fatalf("history for %s = %v, want %v", repo.path, got, want)
	}
	request(t, api, "GET", repo.path+"/commits/"+repo.sha, "", 200, nil)
	request(t, api, "GET", repo.path+"/commits/"+repo.sha+"/files", "", 200, nil)
	request(t, api, "GET", repo.path+"/commits/"+neighbor.sha, "", 404, nil)
	request(t, api, "GET", repo.path+"/commits/"+neighbor.sha+"/files", "", 404, nil)
	request(t, api, "GET", repo.path+"/compare?base="+repo.sha+"&head="+neighbor.sha, "", 404, nil)
	if repo.Name == "software" {
		var compared struct{ Items []struct{ Path string } }
		request(t, api, "GET", repo.path+"/compare?base="+commonSHA+"&head="+repo.sha, "", 200, &compared)
		if len(compared.Items) != 1 || compared.Items[0].Path != repo.file {
			t.Fatalf("comparison for %s = %+v", repo.path, compared)
		}
	}
}

func assertIsolatedActivity(t *testing.T, api *httptest.Server, remote isolationRepository, others ...isolationRepository) {
	t.Helper()
	for _, suffix := range []string{"/issues", "/pulls", "/issues/1/comments", "/pulls/2/comments", "/pulls/2/commits"} {
		var page struct {
			Items []struct {
				RepositoryID uuid.UUID `json:"repository_id"`
			}
		}
		request(t, api, "GET", remote.path+suffix, "", 200, &page)
		if len(page.Items) != 1 || page.Items[0].RepositoryID != remote.ID {
			t.Fatalf("GitHub activity %s = %+v", suffix, page)
		}
		for _, repo := range others {
			if suffix == "/issues" || suffix == "/pulls" {
				request(t, api, "GET", repo.path+suffix, "", 200, &page)
				if len(page.Items) != 0 {
					t.Fatalf("GitHub activity leaked into %s%s: %+v", repo.path, suffix, page)
				}
			} else {
				request(t, api, "GET", repo.path+suffix, "", 404, nil)
			}
		}
	}
	for _, suffix := range []string{"/issues/1", "/pulls/2"} {
		request(t, api, "GET", remote.path+suffix, "", 200, nil)
		for _, repo := range others {
			request(t, api, "GET", repo.path+suffix, "", 404, nil)
		}
	}
}

func assertRepositoryDocuments(t *testing.T, api *httptest.Server, base string, projectDocs []domain.Document, repos ...isolationRepository) {
	t.Helper()
	list := func(path string, want ...domain.Document) {
		t.Helper()
		var page domain.DocumentListResult
		request(t, api, "GET", path, "", 200, &page)
		if page.Total != int64(len(want)) || len(page.Items) != len(want) {
			t.Fatalf("documents at %s = %+v, want %d", path, page, len(want))
		}
		byID := make(map[uuid.UUID]domain.Document, len(want))
		for _, doc := range want {
			byID[doc.ID] = doc
		}
		for _, item := range page.Items {
			doc, ok := byID[item.ID]
			if !ok || item.ProjectID != doc.ProjectID || item.Scope != doc.Scope ||
				item.Title != doc.Title || item.Version != doc.Version || item.ContentBytes != doc.ContentBytes ||
				!sameRepositoryID(item.RepositoryID, doc.RepositoryID) || !sameOrigin(item.Origin, doc.Origin) {
				t.Fatalf("unexpected document summary at %s: %+v", path, item)
			}
			delete(byID, item.ID)
		}
	}
	read := func(path string, want domain.Document) {
		t.Helper()
		var got domain.Document
		request(t, api, "GET", path, "", 200, &got)
		if got.ID != want.ID || got.ProjectID != want.ProjectID || got.Scope != want.Scope ||
			got.Title != want.Title || got.Content != want.Content || got.Version != want.Version ||
			!sameRepositoryID(got.RepositoryID, want.RepositoryID) || !sameOrigin(got.Origin, want.Origin) {
			t.Fatalf("document at %s = %+v, want %+v", path, got, want)
		}
	}
	docs := base + "/documents"
	for _, doc := range projectDocs {
		read(docs+"/"+doc.ID.String(), doc)
	}
	list(docs+"?scope=project", projectDocs...)
	var notes []domain.Document
	for _, repo := range repos {
		note := repo.note
		if note.Scope != domain.KnowledgeScopeRepository || note.RepositoryID == nil || *note.RepositoryID != repo.ID {
			t.Fatalf("document ownership for %s = %+v", repo.path, note)
		}
		notes = append(notes, note)
		list(docs+"?repository_id="+repo.ID.String(), note)
		list(repo.path+"/documents", note)
		read(repo.path+"/documents/"+note.ID.String(), note)
		for _, doc := range projectDocs {
			request(t, api, "GET", repo.path+"/documents/"+doc.ID.String(), "", 404, nil)
		}
		for _, neighbor := range repos {
			if neighbor.ID == repo.ID {
				continue
			}
			path := repo.path + "/documents/" + neighbor.note.ID.String()
			request(t, api, "GET", path, "", 404, nil)
			request(t, api, "PUT", path, `{"title":"Unexpected","version":1}`, 404, nil)
			request(t, api, "PATCH", path, `{"title":"Unexpected","version":9}`, 404, nil)
			request(t, api, "DELETE", path, "", 404, nil)
		}
	}
	list(docs+"?scope=repository", notes...)
	list(docs, append(notes, projectDocs...)...)
}

func sameOrigin(a, b *domain.DocumentOrigin) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// projectDocuments drops unset documents so callers can pass an optional
// project-level document without special casing it.
func projectDocuments(docs ...domain.Document) []domain.Document {
	out := make([]domain.Document, 0, len(docs))
	for _, doc := range docs {
		if doc.ID != uuid.Nil {
			out = append(out, doc)
		}
	}
	return out
}

func sameRepositoryID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func assertForeignRepositoryRequests(t *testing.T, api *httptest.Server, base string, local, foreign isolationRepository) {
	t.Helper()
	wrongPath := base + "/repositories/" + foreign.ID.String()
	projectSearch := base + "/search?q=isolationmarker"
	for _, path := range []string{
		wrongPath, wrongPath + "/snapshots", wrongPath + "/snapshots/active",
		wrongPath + "/files", wrongPath + "/files/content?path=" + local.file,
		wrongPath + "/commits", wrongPath + "/commits/" + local.sha, wrongPath + "/commits/" + local.sha + "/files",
		wrongPath + "/compare?base=" + local.sha + "&head=" + local.sha,
		wrongPath + "/issues", wrongPath + "/pulls", wrongPath + "/sync-policy",
		wrongPath + "/documents", wrongPath + "/documents/" + foreign.note.ID.String(), wrongPath + "/search?q=isolationmarker",
		base + "/documents?repository_id=" + foreign.ID.String(),
		projectSearch + "&repository_id=" + foreign.ID.String(),
		projectSearch + "&repository_ids=" + local.ID.String() + "," + foreign.ID.String(),
	} {
		request(t, api, "GET", path, "", 404, nil)
	}
	request(t, api, "POST", wrongPath+"/documents", `{"title":"Unexpected"}`, 404, nil)
	request(t, api, "POST", base+"/documents", fmt.Sprintf(`{"title":"Unexpected","repository_id":%q}`, foreign.ID), 404, nil)
	request(t, api, "DELETE", wrongPath, "", 404, nil)
}

func assertRepositoryDocumentWrites(t *testing.T, api *httptest.Server, local isolationRepository) {
	t.Helper()
	var note domain.Document
	request(t, api, "POST", local.path+"/documents", `{"title":"Draft"}`, 201, &note)
	notePath := local.path + "/documents/" + note.ID.String()
	request(t, api, "PUT", notePath, `{"title":"Replacement","content":"Updated content","version":1}`, 200, &note)
	if note.Title != "Replacement" || note.Content != "Updated content" || note.Version != 2 {
		t.Fatalf("repository document replacement = %+v", note)
	}
	request(t, api, "PATCH", notePath, `{"title":"Edited","version":2}`, 200, &note)
	if note.Title != "Edited" || note.Content != "Updated content" || note.Version != 3 || note.RepositoryID == nil || *note.RepositoryID != local.ID {
		t.Fatalf("repository document patch = %+v", note)
	}
	request(t, api, "DELETE", notePath, "", 204, nil)
	request(t, api, "GET", notePath, "", 404, nil)
}

func assertIsolationSearch(t *testing.T, result domain.SearchResult, base string, projectDocs []domain.Document, filesOnly bool, repos ...isolationRepository) {
	t.Helper()
	want := make(map[string]isolationRepository)
	for _, repo := range repos {
		want[repo.path+"/files/content?path="+url.QueryEscape(repo.file)] = repo
		if !filesOnly {
			want[repo.path+"/documents/"+repo.note.ID.String()] = repo
		}
	}
	if len(projectDocs) != 0 {
		for _, doc := range projectDocs {
			want[base+"/documents/"+doc.ID.String()] = isolationRepository{note: doc}
		}
	}
	if result.Total != int64(len(want)) || len(result.Items) != len(want) {
		t.Fatalf("search returned %d hits (%d total), want %d: %+v", len(result.Items), result.Total, len(want), result.Items)
	}
	for _, hit := range result.Items {
		loc := hit.Location
		repo, ok := want[loc.APIPath]
		if !ok {
			t.Fatalf("unexpected hit: %+v", hit)
		}
		delete(want, loc.APIPath)
		if repo.ID == uuid.Nil {
			if loc.Scope != domain.KnowledgeScopeProject || loc.RepositoryID != nil ||
				loc.RepositoryRole != "" || loc.RepositoryName != "" || loc.RepositorySource != "" {
				t.Fatalf("project hit provenance: %+v", hit)
			}
		} else if hit.ProjectID != repo.ProjectID || loc.Scope != domain.KnowledgeScopeRepository ||
			loc.RepositoryID == nil || *loc.RepositoryID != repo.ID || loc.RepositoryRole != repo.Role ||
			loc.RepositoryName != repo.Name || loc.RepositorySource != repo.Source {
			t.Fatalf("repository hit provenance: %+v", hit)
		}
		if strings.Contains(loc.APIPath, "/files/content?") {
			if hit.Kind != domain.SearchHitKindSourceFile || hit.SourceType != domain.SearchSourceType(repo.SourceType) ||
				loc.Path != repo.file || loc.CommitHash != repo.sha || loc.SnapshotID == nil || *loc.SnapshotID != repo.snapshot.SnapshotID ||
				loc.SnapshotVersion == nil || *loc.SnapshotVersion != repo.snapshot.Version {
				t.Fatalf("file hit provenance: %+v", hit)
			}
		} else if hit.Kind != domain.SearchHitKindDocument || hit.SourceType != domain.SearchSourceDocument ||
			hit.ProjectID != repo.note.ProjectID || hit.ID != repo.note.ID ||
			loc.DocumentID == nil || *loc.DocumentID != repo.note.ID || loc.DocumentTitle != repo.note.Title ||
			loc.DocumentVersion == nil || *loc.DocumentVersion != repo.note.Version {
			t.Fatalf("document hit provenance: %+v", hit)
		}
	}
}

func assertDisassociatedData(t *testing.T, f *syncFixture, id uuid.UUID) {
	t.Helper()
	for _, table := range []string{
		"snapshots", "source_files", "repository_commits", "repository_history_refs",
		"github_activity", "github_activity_state", "sync_policies",
	} {
		var count int
		if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table+" WHERE repository_id = $1", id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s has %d rows for disassociated repository %s: %v", table, count, id, err)
		}
	}
	// Documents are user-authored notes: they survive as project-level documents
	// that keep the repository as informational provenance.
	var scoped, preserved int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM documents WHERE repository_id = $1", id).Scan(&scoped); err != nil || scoped != 0 {
		t.Fatalf("documents still scoped to disassociated repository %s: %d rows, %v", id, scoped, err)
	}
	if err := f.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM documents WHERE repository_id IS NULL AND metadata->'origin'->>'repository_id' = $1",
		id.String()).Scan(&preserved); err != nil || preserved == 0 {
		t.Fatalf("documents of disassociated repository %s were not preserved with origin: %d rows, %v", id, preserved, err)
	}
}
