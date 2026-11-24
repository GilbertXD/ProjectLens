package gitscan

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

func TestRemoteManifestParity(t *testing.T) {
	dir := newFixture(t)
	writeFixture(t, dir, "z.sh", "run\n", 0755)
	writeFixture(t, dir, "a.txt", "first\nsecond", 0644)
	writeFixture(t, dir, "binary", "a\x00b", 0644)
	writeFixture(t, dir, "vendor/dependency", "ignored", 0644)
	fixtureGit(t, dir, "add", ".")
	parent := saveFixture(t, dir)
	head := saveFixture(t, dir, parent)
	fixtureGit(t, dir, "tag", "snapshot", head)
	web := t.TempDir()
	bare := filepath.Join(web, "owner", "project.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "clone", "--bare", dir, bare)
	fixtureGit(t, bare, "update-server-info")
	files := http.FileServer(http.Dir(web))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("GitHub credentials reached the mirror")
		}
		files.ServeHTTP(w, r)
	}))
	defer server.Close()

	cfg := config.DefaultScannerConfig()
	cfg.CacheDir = t.TempDir()
	scanner := NewScanner(cfg, config.RepositoryConfig{AllowedLocalRoots: []string{dir}},
		config.GitHubConfig{GitBaseURL: server.URL, Token: "must-stay-on-github"})
	options := ScanOptions{IncludeContent: true}
	local, err := scanner.Scan(t.Context(), SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: dir}, options)
	if err != nil {
		t.Fatal(err)
	}

	remote := SourceSpec{SourceType: domain.RepositorySourceGitHub, Source: "https://www.github.com/Owner/Project.git"}
	var group sync.WaitGroup
	for _, ref := range []string{"", "main", "refs/tags/snapshot", head} {
		group.Go(func() {
			spec := remote
			spec.Ref = ref
			got, err := scanner.Scan(t.Context(), spec, options)
			if err != nil {
				t.Error(err)
				return
			}
			if got.CanonicalSource != "https://github.com/owner/project" || got.SourceType != domain.RepositorySourceGitHub ||
				!reflect.DeepEqual(got.Commit, local.Commit) || !reflect.DeepEqual(got.Files, local.Files) ||
				!reflect.DeepEqual(got.Skipped, local.Skipped) || !reflect.DeepEqual(got.Stats, local.Stats) {
				t.Errorf("manifest mismatch\nlocal: %+v\nremote: %+v", local, got)
			}
		})
	}
	group.Wait()
	page, err := scanner.History(t.Context(), remote, 100, func(ids []string) (map[string]bool, error) {
		m := map[string]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m, nil
	}, func(c HistoryCommit, changes Changes) error {
		if !strings.HasPrefix(c.URL, "https://github.com/owner/project/commit/") {
			t.Fatal(c.URL)
		}
		return nil
	})
	if err != nil || page.Imported != 2 || page.Tip != head {
		t.Fatalf("remote history=%+v err=%v", page, err)
	}
	entries, err := os.ReadDir(cfg.CacheDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("expected one cache and its lock: %v, %v", entries, err)
	}
	for _, entry := range entries {
		if entry.IsDir() && fixtureGit(t, filepath.Join(cfg.CacheDir, entry.Name()), "rev-parse", "--is-bare-repository") != "true" {
			t.Fatal("cache is not bare")
		}
	}
	remote.Ref = "-x"
	if _, err := scanner.Scan(t.Context(), remote, options); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid remote ref: %v", err)
	}
}

func TestRemoteCompareColdCache(t *testing.T) {
	dir := newFixture(t)
	writeFixture(t, dir, "a.txt", "original\n", 0644)
	fixtureGit(t, dir, "add", "a.txt")
	base := saveFixture(t, dir)
	writeFixture(t, dir, "a.txt", "updated\n", 0644)
	fixtureGit(t, dir, "add", "a.txt")
	head := saveFixture(t, dir, base)
	// The mirror advertises only this newer tip.
	writeFixture(t, dir, "a.txt", "newer\n", 0644)
	fixtureGit(t, dir, "add", "a.txt")
	saveFixture(t, dir, head)

	web := t.TempDir()
	bare := filepath.Join(web, "owner", "project.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "clone", "--bare", dir, bare)
	fixtureGit(t, bare, "update-server-info")
	files := http.FileServer(http.Dir(web))
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		files.ServeHTTP(w, r)
	}))
	defer server.Close()

	cfg := config.DefaultScannerConfig()
	cfg.CacheDir = t.TempDir()
	scanner := NewScanner(cfg, config.RepositoryConfig{}, config.GitHubConfig{GitBaseURL: server.URL})
	spec := SourceSpec{SourceType: domain.RepositorySourceGitHub, Source: "https://github.com/owner/project"}
	changes, err := scanner.Compare(t.Context(), spec, base, head)
	if err != nil || len(changes.Files) != 1 || changes.Files[0].Path != "a.txt" || !strings.Contains(changes.Patch, "+updated") {
		t.Fatalf("cold-cache compare = %+v, %v", changes, err)
	}
	fetched := requests.Load()
	cached, err := scanner.Compare(t.Context(), spec, base, head)
	if err != nil || !reflect.DeepEqual(changes, cached) || fetched == 0 || requests.Load() != fetched {
		t.Fatalf("warm-cache compare = %+v, %v, requests before=%d after=%d", cached, err, fetched, requests.Load())
	}
}

func TestRemoteRejectsNonGitHubSources(t *testing.T) {
	source := NewRemoteSource(t.TempDir(), "", "http://mirror.example")
	for _, raw := range []string{"http://github.com/a/b", "https://user:secret@github.com/a/b", "https://github.com.example/a/b", "file:///tmp/test"} {
		if _, err := source.Prepare(t.Context(), SourceSpec{Source: raw}); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("validating %q: %v", raw, err)
		}
	}
}

func TestGitTokenStaysInEnvironmentAndOutOfErrors(t *testing.T) {
	const token = "github_pat_scanner_fixture"
	dir := t.TempDir()
	script := filepath.Join(dir, "git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$GIT_CONFIG_VALUE_0\" >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_TRACE", "1")
	cache := t.TempDir()
	target := filepath.Join(cache, "test.git")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	runner := gitRunner{token: token, cacheDir: cache}
	args := []string{"--git-dir=" + target, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", "--", "https://github.com/owner/project.git", "HEAD"}
	cmd := runner.command(context.Background(), true, args...)
	if cmd.Err != nil {
		t.Fatal(cmd.Err)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), token) || !strings.Contains(strings.Join(cmd.Env, "\n"), "Authorization: Bearer "+token) {
		t.Fatal("the token must be passed in the command environment")
	}
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "GIT_TRACE=") {
			t.Fatal("inherited Git tracing is enabled")
		}
	}
	_, err := runner.run(t.Context(), 1024, true, args...)
	if err == nil || strings.Contains(gitDiagnostic(err), token) || !strings.Contains(gitDiagnostic(err), "[redacted]") || strings.Contains(err.Error(), token) ||
		strings.Contains(remoteError(err).Error(), token) {
		t.Fatal("token unexpectedly surfaced in an error")
	}
}

func TestCacheLockSerializesAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.lock")
	release, err := lockCache(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if second, err := lockCache(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		if second != nil {
			second()
		}
		t.Fatalf("lock was not exclusive: %v", err)
	}
	release()
	second, err := lockCache(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second()
}

func TestRemoteFetchFallback(t *testing.T) {
	dir := t.TempDir()
	const script = `#!/bin/sh
for arg do
	if [ "$arg" = "--depth=1" ]; then
		echo "fatal: dumb http transport does not support shallow capabilities" >&2
		exit 1
	fi
done
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	target := filepath.Join(dir, "test.git")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := fetchCommit(t.Context(), gitRunner{cacheDir: dir}, target, "https://github.com/owner/project.git", "HEAD", 1); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteErrorRateLimited(t *testing.T) {
	for _, diagnostic := range []string{
		"The requested URL returned error: 429",
		"HTTP 429",
		"Too many requests",
		"API rate limit exceeded (error: 403)",
		"remote: rate-limit exceeded",
	} {
		err := remoteError(&gitError{err: errors.New("exit status 128"), diagnostic: diagnostic})
		if !errors.Is(err, domain.ErrRateLimited) {
			t.Errorf("remoteError for %q = %v, want ErrRateLimited", diagnostic, err)
		}
	}
}
