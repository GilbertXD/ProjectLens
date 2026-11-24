package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
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
