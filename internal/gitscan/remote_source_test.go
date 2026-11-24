package gitscan

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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

func TestHistoricalRemoteCache(t *testing.T) {
	ctx := t.Context()
	dir := newFixture(t)
	writeFixture(t, dir, "file.txt", "base\n", 0644)
	fixtureGit(t, dir, "add", ".")
	first := saveFixture(t, dir)
	writeFixture(t, dir, "file.txt", "base\nnew\n", 0644)
	fixtureGit(t, dir, "add", ".")
	tip := saveFixture(t, dir, first)
	fixtureGit(t, dir, "tag", "-a", "v1", "-m", "release", first)
	tagOID := fixtureGit(t, dir, "rev-parse", "refs/tags/v1")

	// Use real Git's shallow fetch protocol over a fixture path. Simulate
	// transport failures and unadvertised-object restrictions in the shim.
	shim := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	const script = `#!/bin/sh
prev=
depth=
for arg do
	case "$arg" in
		--git-dir=*) gitdir=$arg ;;
		fetch) operation=fetch ;;
		ls-remote) operation=ls-remote ;;
		--depth=*) depth=$arg ;;
	esac
	prev=$arg
done
ref=$prev
case "$operation" in
	fetch|ls-remote)
		printf '%s %s %s\n' "$operation" "$depth" "$ref" >> "$PROJECTLENS_TEST_REQUESTS"
		case "$PROJECTLENS_TEST_REMOTE_STATE" in
			offline) echo "Failed to connect" >&2; exit 128 ;;
			limited) echo "HTTP 429" >&2; exit 128 ;;
		esac ;;
esac
if [ "$operation" = fetch ]; then
	if [ "${#ref}" -eq 40 ] && [ "$ref" != "$PROJECTLENS_TEST_TIP" ]; then
		echo "not our ref" >&2
		exit 128
	fi
	export GIT_ALLOW_PROTOCOL=file
	exec "$PROJECTLENS_TEST_GIT" "$gitdir" fetch --no-tags "$depth" -- "file://$PROJECTLENS_TEST_REMOTE" "$ref"
fi
if [ "$operation" = ls-remote ]; then
	export GIT_ALLOW_PROTOCOL=file
	exec "$PROJECTLENS_TEST_GIT" ls-remote --symref -- "file://$PROJECTLENS_TEST_REMOTE" HEAD 'refs/heads/*' 'refs/tags/*'
fi
exec "$PROJECTLENS_TEST_GIT" "$@"
`
	requestFile := filepath.Join(shim, "requests")
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJECTLENS_TEST_GIT", git)
	t.Setenv("PROJECTLENS_TEST_REMOTE", dir)
	t.Setenv("PROJECTLENS_TEST_TIP", tip)
	t.Setenv("PROJECTLENS_TEST_REQUESTS", requestFile)
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	requests := func() string {
		t.Helper()
		raw, err := os.ReadFile(requestFile)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	cfg := config.DefaultScannerConfig()
	cfg.CacheDir = t.TempDir()
	s := NewScanner(cfg, config.RepositoryConfig{}, config.GitHubConfig{Token: "test"})
	spec := SourceSpec{SourceType: domain.RepositorySourceGitHub, Source: "https://github.com/owner/project"}
	file, err := s.FileAtCommit(ctx, spec, tip, "file.txt")
	if err != nil || file.Content != "base\nnew\n" || !strings.Contains(requests(), "--depth=1 "+tip) {
		t.Fatalf("cold pinned file = %+v, %v", file, err)
	}
	before := requests()
	t.Setenv("PROJECTLENS_TEST_REMOTE_STATE", "offline")
	if tree, err := s.TreeAtCommit(ctx, spec, tip, "", 20, 0); err != nil || tree.Total != 1 {
		t.Fatalf("cached tree = %+v, %v", tree, err)
	}
	if warm, err := s.FileAtCommit(ctx, spec, tip, "file.txt"); err != nil || warm != file || before != requests() {
		t.Fatalf("cached file = %+v, %v, requests=%q", warm, err, requests())
	}
	if _, err := s.FileAtCommit(ctx, spec, first, "file.txt"); !errors.Is(err, domain.ErrUpstreamUnavailable) {
		t.Fatalf("missing commit with offline remote = %v", err)
	}
	if _, err := s.FileHistory(ctx, spec, tip, "file.txt", true, 20, 0); !errors.Is(err, domain.ErrUpstreamUnavailable) {
		t.Fatalf("shallow history with offline remote = %v", err)
	}
	if _, err := s.Blame(ctx, spec, tip, "file.txt", 0, 0); !errors.Is(err, domain.ErrUpstreamUnavailable) {
		t.Fatalf("shallow blame with offline remote = %v", err)
	}
	t.Setenv("PROJECTLENS_TEST_REMOTE_STATE", "limited")
	if _, err := s.FileAtCommit(ctx, spec, first, "file.txt"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("remote rate limit = %v", err)
	}
	t.Setenv("PROJECTLENS_TEST_REMOTE_STATE", "")
	history, err := s.FileHistory(ctx, spec, tip, "file.txt", true, 20, 0)
	if err != nil || !history.HistoryComplete || history.Shallow || len(history.Items) != 2 || history.Items[1].Hash != first ||
		history.Items[1].URL != spec.Source+"/commit/"+first ||
		!strings.Contains(requests(), "--depth="+strconv.Itoa(HistoryLimit+2)+" "+tip) {
		t.Fatalf("deepened history = %+v, %v", history, err)
	}
	refs, err := s.ListRefs(ctx, spec, "tag", 20, 0)
	if err != nil || refs.DefaultBranch != "main" || refs.Total != 1 || refs.Items[0].ObjectHash != tagOID || refs.Items[0].CommitHash != first {
		t.Fatalf("remote refs = %+v, %v", refs, err)
	}
	before = requests()
	t.Setenv("PROJECTLENS_TEST_REMOTE_STATE", "offline")
	blame, err := s.Blame(ctx, spec, tip, "file.txt", 0, 0)
	if err != nil || blame.Shallow || len(blame.Lines) != 2 || blame.Lines[0].CommitHash != first || before != requests() {
		t.Fatalf("cached blame after deepening = %+v, %v", blame, err)
	}
	t.Setenv("PROJECTLENS_TEST_REMOTE_STATE", "")
	cfg.CacheDir = t.TempDir()
	s = NewScanner(cfg, config.RepositoryConfig{}, config.GitHubConfig{})
	old, err := s.FileAtCommit(ctx, spec, first, "file.txt")
	if err != nil || old.Content != "base\n" || !strings.Contains(requests(), "--depth="+strconv.Itoa(HistoryLimit+2)+" HEAD") {
		t.Fatalf("unadvertised commit fallback = %+v, %v", old, err)
	}
	if _, err := s.TreeAtCommit(ctx, spec, strings.Repeat("0", 40), "", 20, 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing commit after successful fallback = %v", err)
	}
}

func TestHistoricalShallowBoundaries(t *testing.T) {
	dir := newFixture(t)
	writeFixture(t, dir, "file", "initial\n", 0644)
	fixtureGit(t, dir, "add", ".")
	first := saveFixture(t, dir)
	writeFixture(t, dir, "file", "initial\nnew\n", 0644)
	fixtureGit(t, dir, "add", ".")
	tip := saveFixture(t, dir, first)
	root := t.TempDir()
	shallow := filepath.Join(root, "shallow")
	fixtureGit(t, dir, "clone", "--depth=1", "--", "file://"+dir, shallow)
	s := localScanner(root, config.DefaultScannerConfig())
	spec := SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: shallow}
	page, err := s.History(t.Context(), spec, HistoryPage, func(ids []string) (map[string]bool, error) {
		return map[string]bool{tip: true}, nil
	}, func(commit HistoryCommit, changes Changes) error {
		if commit.Hash != tip || !reflect.DeepEqual(commit.Parents, []string{first}) ||
			!changes.ParentUnavailable || !changes.Truncated || len(changes.Files) != 0 {
			t.Fatalf("shallow import = %+v, %+v", commit, changes)
		}
		return nil
	})
	if err != nil || page.Imported != 1 || page.ReachableCount != 1 || !page.HasMore ||
		page.Complete || !page.Truncated || page.WindowStatus != "partial" {
		t.Fatalf("shallow import window = %+v, %v", page, err)
	}
	history, err := s.FileHistory(t.Context(), spec, tip, "file", true, 20, 0)
	if err != nil || !history.Shallow || !history.Truncated || history.HistoryComplete ||
		len(history.Items) != 1 || !reflect.DeepEqual(history.Items[0].Parents, []string{first}) {
		t.Fatalf("shallow file history = %+v, %v", history, err)
	}
	blame, err := s.Blame(t.Context(), spec, tip, "file", 2, 2)
	if err != nil || !blame.Shallow || !blame.BoundaryReached || blame.HistoryComplete ||
		len(blame.Lines) != 1 || !blame.Lines[0].Boundary || blame.Lines[0].CommitHash != tip {
		t.Fatalf("shallow blame = %+v, %v", blame, err)
	}
}
