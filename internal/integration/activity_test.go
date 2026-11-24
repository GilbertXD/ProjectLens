package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"projectlens/internal/config"
	"projectlens/internal/domain"
	"projectlens/internal/github"
	"projectlens/internal/handler"
	"projectlens/internal/router"
	"projectlens/internal/service"
)

func TestActivityReconciliation(t *testing.T) {
	f := newSyncFixture(t)
	owner, name := "owner", "project"
	id := int64(42)
	f.attach(t, domain.Repository{SourceType: domain.RepositorySourceGitHub, Source: "https://github.com/owner/project", GitHubOwner: &owner, GitHubRepo: &name, GitHubRepoID: &id})
	const root = "/repos/owner/project"
	var failure, phase, conditional atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := phase.Load()
		item := func(id, num int, extra map[string]any) map[string]any {
			a := map[string]any{"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z", "id": id, "number": num, "html_url": "https://github.com/owner/project/issues/1"}
			for k, v := range extra {
				a[k] = v
			}
			return a
		}
		var body any = []any{}
		switch strings.TrimPrefix(r.URL.Path, root) {
		case "":
			body = map[string]any{"id": 42, "full_name": "owner/project"}
		case "/issues":
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("Link", `<next>; rel="next"`)
				if n < 2 {
					body = []any{item(100, 1, nil)}
				}
			} else {
				body = []any{item(101, 2, map[string]any{"pull_request": map[string]any{}}), item(101, 2, map[string]any{"pull_request": map[string]any{}})}
			}
		case "/pulls/2":
			if r.Header.Get("If-None-Match") == "abc" {
				conditional.Add(1)
				w.WriteHeader(304)
				return
			}
			w.Header().Set("ETag", "abc")
			body = item(201, 2, map[string]any{"commits": 1})
		case "/pulls/2/commits":
			body = []any{item(0, 0, map[string]any{"sha": strings.Repeat("a", 40)})}
		case "/issues/comments":
			if n < 2 {
				body = []any{item(301, 0, map[string]any{"issue_url": "https://api.github.com" + root + "/issues/1", "body": fmt.Sprint("comment", n)})}
			}
		case "/pulls/comments":
			if code := int(failure.Load()); code != 0 {
				w.Header().Set("Retry-After", "121")
				w.WriteHeader(code)
				return
			}
			body = []any{item(401, 0, map[string]any{"pull_request_url": "https://api.github.com" + root + "/pulls/2"})}
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	s := service.ActivityService{Repositories: f.repos, Locks: f.store, Tx: f.tx, Client: github.NewClient(config.GitHubConfig{APIBaseURL: srv.URL}, srv.Client())}
	target := domain.SyncTarget{ProjectID: f.projectID, RepositoryID: f.repoID}
	syncOK := func() {
		if _, err := s.Sync(t.Context(), target); err != nil {
			t.Fatal(err)
		}
	}
	count := func(want int) {
		var n int
		if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM github_activity").Scan(&n); err != nil || n != want {
			t.Fatal(n, err)
		}
	}
	failure.Store(500)
	if _, err := s.Sync(t.Context(), target); !errors.Is(err, domain.ErrUpstreamUnavailable) {
		t.Fatal(err)
	}
	count(0)
	failure.Store(0)
	syncOK()
	count(5)
	api := httptest.NewServer(router.New(f.cfg, f.log, router.Dependencies{Activity: &handler.ActivityHandler{Store: f.repos, Log: f.log}}))
	defer api.Close()
	projectPath := "/api/v1/projects/" + f.projectID.String()
	repoPath := projectPath + "/repositories/" + f.repoID.String()
	for _, route := range []string{projectPath + "/issues", projectPath + "/pulls", repoPath + "/issues/1/comments", repoPath + "/pulls/2/comments", repoPath + "/pulls/2/commits"} {
		var page struct{ Items []json.RawMessage }
		request(t, api, "GET", route, "", 200, &page)
		if len(page.Items) != 1 {
			t.Fatal(route, page.Items)
		}
	}
	request(t, api, "GET", repoPath+"/issues/2", "", 404, nil)
	request(t, api, "GET", repoPath+"/pulls/1/comments", "", 404, nil)
	cursor, _, _ := f.repos.ActivityCursor(t.Context(), f.repoID)
	if !cursor.Equal(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("cursor=%v", cursor)
	}
	phase.Store(1)
	failure.Store(429)
	_, err := s.Sync(t.Context(), target)
	var rate *github.RetryError
	if !errors.As(err, &rate) || rate.Delay != 121*time.Second {
		t.Fatal(err)
	}
	after, _, _ := f.repos.ActivityCursor(t.Context(), f.repoID)
	if !after.Equal(cursor) {
		t.Fatal("cursor advanced after failure")
	}
	failure.Store(0)
	syncOK()
	count(5)
	if conditional.Load() == 0 {
		t.Fatal("missing conditional request")
	}
	records, err := f.repos.ReadActivity(t.Context(), f.projectID, &f.repoID, []string{"comment"}, 1, 10, 0)
	if err != nil || len(records) != 1 || !strings.Contains(string(records[0]), "comment1") {
		t.Fatal(records, err)
	}
	phase.Store(2)
	if _, err := f.pool.Exec(t.Context(), "UPDATE github_activity_state SET reconciled_at=now()-interval '2 days'"); err != nil {
		t.Fatal(err)
	}
	syncOK()
	count(3)
}

func TestLiveGitHubActivity(t *testing.T) {
	raw := os.Getenv("TEST_ACTIVITY_REMOTE_REPO")
	if raw == "" {
		t.Skip("TEST_ACTIVITY_REMOTE_REPO is unset")
	}
	owner, name, canonical, err := github.ParsePublicRepoURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	f := newSyncFixture(t)
	client := github.NewClient(config.GitHubConfig{APIBaseURL: "https://api.github.com", Timeout: 10 * time.Second, UserAgent: "ProjectLens-smoke"}, nil)
	meta, err := client.Fetch(t.Context(), owner, name)
	if err != nil {
		t.Fatal(err)
	}
	f.attach(t, domain.Repository{SourceType: domain.RepositorySourceGitHub, Source: canonical, GitHubOwner: &owner, GitHubRepo: &name, GitHubRepoID: &meta.ID})
	activity := service.ActivityService{Repositories: f.repos, Locks: f.store, Client: client, Tx: f.tx}
	count, err := activity.Sync(t.Context(), domain.SyncTarget{ProjectID: f.projectID, RepositoryID: f.repoID})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("upserted %d records", count)
}
