package gitscan

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Scanner Test", "GIT_AUTHOR_EMAIL=scanner@example.org",
		"GIT_COMMITTER_NAME=Scanner Test", "GIT_COMMITTER_EMAIL=scanner@example.org",
		"GIT_AUTHOR_DATE=2024-01-01T01:00:00+02:00", "GIT_COMMITTER_DATE=2024-01-01T02:00:00+02:00",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSuffix(string(out), "\n")
}

func newFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "--initial-branch=main", "--template=")
	return dir
}

func writeFixture(t *testing.T, root, name, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func saveFixture(t *testing.T, dir string, parents ...string) string {
	t.Helper()
	tree := fixtureGit(t, dir, "write-tree")
	args := []string{"commit-tree", tree, "-m", "snapshot fixture"}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	oid := fixtureGit(t, dir, args...)
	fixtureGit(t, dir, "update-ref", "HEAD", oid)
	return oid
}

func localScanner(root string, cfg config.ScannerConfig) *Scanner {
	return NewScanner(cfg, config.RepositoryConfig{AllowedLocalRoots: []string{root}}, config.GitHubConfig{})
}

func TestLocalSnapshotPinsCommittedState(t *testing.T) {
	dir := newFixture(t)
	writeFixture(t, dir, "b.sh", "one\ntwo", 0755)
	writeFixture(t, dir, "a.txt", "original\n", 0644)
	fixtureGit(t, dir, "add", ".")
	first := saveFixture(t, dir)
	writeFixture(t, dir, "a.txt", "baseline\n", 0644)
	fixtureGit(t, dir, "add", ".")
	head := saveFixture(t, dir, first)
	writeFixture(t, dir, "a.txt", "dirty\n", 0644)
	writeFixture(t, dir, "b.sh", "staged\n", 0755)
	fixtureGit(t, dir, "add", "b.sh")
	writeFixture(t, dir, "new.txt", "untracked", 0644)
	status := fixtureGit(t, dir, "status", "--porcelain")

	paths := []string{"a.txt", "b.sh", "new.txt", ".git/HEAD", ".git/index"}
	before := make([]os.FileInfo, len(paths))
	for i, path := range paths {
		var err error
		before[i], err = os.Stat(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
	}

	scanner := localScanner(dir, config.DefaultScannerConfig())
	spec := SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: dir}
	snap, err := scanner.Scan(t.Context(), spec, ScanOptions{IncludeContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Commit.Hash != head || !reflect.DeepEqual(snap.Commit.Parents, []string{first}) ||
		snap.ResolvedRef != "HEAD" || len(snap.Files) != 2 || snap.Stats.IncludedBytes != 16 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	for i, want := range []struct {
		path, content string
		lines         int
		executable    bool
	}{
		{"a.txt", "baseline\n", 1, false},
		{"b.sh", "one\ntwo", 2, true},
	} {
		file := snap.Files[i]
		oid := fixtureGit(t, dir, "rev-parse", head+":"+want.path)
		if file.Path != want.path || file.BlobOID != oid || file.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(want.content))) ||
			file.Size != int64(len(want.content)) || file.LineCount != want.lines || string(file.Content) != want.content || file.IsExecutable != want.executable {
			t.Fatalf("unexpected file: %+v", file)
		}
		data, err := scanner.ReadBlob(t.Context(), spec, head, oid)
		if err != nil || string(data) != want.content {
			t.Fatalf("ReadBlob returned %q, %v", data, err)
		}
	}
	oldOID := fixtureGit(t, dir, "rev-parse", first+":a.txt")
	if _, err := scanner.ReadBlob(t.Context(), spec, head, oldOID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("read blob from another commit: %v", err)
	}
	if got := fixtureGit(t, dir, "status", "--porcelain"); got != status || fixtureGit(t, dir, "rev-parse", "HEAD") != head {
		t.Fatalf("repository changed: %q -> %q", status, got)
	}
	for i, path := range paths {
		after, err := os.Stat(filepath.Join(dir, path))
		if err != nil || !after.ModTime().Equal(before[i].ModTime()) {
			t.Fatalf("mtime changed for %s: %v", path, err)
		}
	}

	copyDir := t.TempDir()
	fixtureGit(t, copyDir, "clone", "--no-local", dir, ".")
	var directories []string
	if err := filepath.WalkDir(copyDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, path := range directories {
			if err := os.Chmod(path, 0700); err != nil {
				t.Error(err)
			}
		}
	})
	for _, path := range directories {
		if err := os.Chmod(path, 0500); err != nil {
			t.Fatal(err)
		}
	}
	spec.Source = copyDir
	readOnly, err := localScanner(copyDir, config.DefaultScannerConfig()).Scan(t.Context(), spec, ScanOptions{})
	if err != nil || readOnly.Commit.Hash != head || len(readOnly.Files) != 2 || readOnly.Files[0].Content != nil {
		t.Fatalf("read-only scan: %+v, %v", readOnly, err)
	}
}

func TestLocalFilteringAndBudgets(t *testing.T) {
	dir := newFixture(t)
	files := map[string]string{
		"a.txt": "12345", "b.txt": "67890", "c.txt": "four",
		"big.txt": "1234567", "binary.txt": "a\x00b", "invalid.txt": "\xff",
		"deep/more/file": "x", "very-long-file-name.txt": "x", "unsafe\\name": "x",
		"node_modules/pkg": "x", "vendor/pkg": "x", "other/custom.tmp": "x",
	}
	for name, content := range files {
		writeFixture(t, dir, name, content, 0644)
	}
	if err := os.Symlink(filepath.Dir(dir), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, dir, "add", ".")
	base := saveFixture(t, dir)
	fixtureGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+base+",gitlink")
	saveFixture(t, dir, base)
	cfg := config.DefaultScannerConfig()
	cfg.MaxFileSize, cfg.MaxTotalBytes, cfg.MaxFiles = 6, 9, 2
	cfg.MaxPathLength, cfg.MaxPathDepth = 16, 2
	cfg.ExcludePatterns = []string{"*.tmp"}
	spec := SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: dir}
	snap, err := localScanner(dir, cfg).Scan(t.Context(), spec, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"file_too_large": 1, "binary_file": 1, "max_files_exceeded": 1, "path_limit_exceeded": 2,
		"unsafe_path": 1, "ignored_path": 3, "symlink": 1, "submodule": 1, "total_bytes_exceeded": 1,
	}
	if !reflect.DeepEqual(snap.Stats.SkippedByReason, want) || !snap.Stats.Truncated ||
		snap.Stats.TotalTreeEntries != len(files)+2 || snap.Stats.IncludedBytes != 9 ||
		len(snap.Files) != 2 || snap.Files[0].Path != "a.txt" || snap.Files[1].Path != "c.txt" {
		t.Fatalf("unexpected filtering: %+v", snap)
	}
	cfg.MaxFiles, cfg.MaxTotalBytes = 20, 60
	snap, err = localScanner(dir, cfg).Scan(t.Context(), spec, ScanOptions{})
	if err != nil || snap.Stats.SkippedByReason["binary_file"] != 2 {
		t.Fatalf("invalid UTF-8 and NUL filtering: %+v, %v", snap, err)
	}
	if isBinary([]byte(strings.Repeat("a", 8191)+"é")) || !isBinary([]byte(strings.Repeat("a", 8191)+"\xc3")) {
		t.Fatal("invalid UTF-8 sniff boundary handling")
	}
	for _, name := range []string{"", "/absolute", "x/../y", "./x", ".git/config", "x/\ty"} {
		entry := treeEntry{SkippedFile: SkippedFile{Mode: "100644", Path: name}, kind: "blob"}
		if got := filterEntry(entry, cfg); got != SkipUnsafePath {
			t.Errorf("filter %q: %q", name, got)
		}
	}
}

func TestSkippedCountRemainsExact(t *testing.T) {
	snap := Snapshot{Stats: SnapshotStats{SkippedByReason: make(map[string]int)}}
	for range skippedLimit + 1 {
		snap.skip(treeEntry{}, SkipMaxFilesExceeded)
	}
	if len(snap.Skipped) != skippedLimit || !snap.Stats.SkippedListTruncated ||
		snap.Stats.SkippedFiles != skippedLimit+1 || snap.Stats.SkippedByReason[string(SkipMaxFilesExceeded)] != skippedLimit+1 {
		t.Fatalf("unexpected skip totals: %+v", snap.Stats)
	}
}

func TestLocalRejectsInvalidSource(t *testing.T) {
	dir := newFixture(t)
	spec := SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: dir}
	for _, ref := range []string{"", "--upload-pack=command", "-x", "foo..bar", "HEAD@{1}", "bad ref", "bad\x00ref"} {
		spec.Ref = ref
		if _, err := localScanner(dir, config.DefaultScannerConfig()).Scan(t.Context(), spec, ScanOptions{}); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("ref %q: %v", ref, err)
		}
	}
	spec.Ref = ""
	if _, err := localScanner(t.TempDir(), config.DefaultScannerConfig()).Scan(t.Context(), spec, ScanOptions{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("disallowed local root: %v", err)
	}
	linked := t.TempDir()
	writeFixture(t, linked, ".git", "gitdir: "+filepath.Join(dir, ".git")+"\n", 0644)
	spec.Source = linked
	if _, err := localScanner(linked, config.DefaultScannerConfig()).Scan(t.Context(), spec, ScanOptions{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("disallowed Git directory: %v", err)
	}
}

func TestShallowCommitMetadata(t *testing.T) {
	dir := newFixture(t)
	writeFixture(t, dir, "a", "a", 0644)
	fixtureGit(t, dir, "add", ".")
	parent := saveFixture(t, dir)
	head := saveFixture(t, dir, parent)
	repo := &PreparedRepo{GitDir: filepath.Join(dir, ".git"), PinnedCommit: head}
	full, err := readCommit(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if full.AuthoredAt != time.Date(2023, 12, 31, 23, 0, 0, 0, time.UTC) ||
		full.CommittedAt != time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("expected UTC commit timestamps, got authored=%v committed=%v", full.AuthoredAt, full.CommittedAt)
	}
	writeFixture(t, repo.GitDir, "shallow", head+"\n", 0644)
	shallow, err := readCommit(t.Context(), repo)
	if err != nil || !reflect.DeepEqual(full, shallow) {
		t.Fatalf("shallow metadata differs: full=%+v shallow=%+v err=%v", full, shallow, err)
	}
}

func TestScannerConfigHash(t *testing.T) {
	cfg := config.DefaultScannerConfig()
	cfg.ExcludePatterns = []string{"z", "a"}
	hash := localScanner(t.TempDir(), cfg).ConfigHash()
	if !validOID(hash) || len(hash) != 64 {
		t.Fatalf("invalid configuration hash: %q", hash)
	}
	for _, mutate := range []func(*config.ScannerConfig){
		func(cfg *config.ScannerConfig) { cfg.MaxFileSize++ },
		func(cfg *config.ScannerConfig) { cfg.MaxTotalBytes++ },
		func(cfg *config.ScannerConfig) { cfg.MaxFiles++ },
		func(cfg *config.ScannerConfig) { cfg.MaxPathLength++ },
		func(cfg *config.ScannerConfig) { cfg.MaxPathDepth++ },
		func(cfg *config.ScannerConfig) { cfg.ExcludePatterns = []string{"a", "b"} },
	} {
		changed := cfg
		mutate(&changed)
		if localScanner("", changed).ConfigHash() == hash {
			t.Fatalf("changed filters produced the same hash: %+v", changed)
		}
	}
	cfg.ExcludePatterns = []string{"a", "z"}
	cfg.CacheDir, cfg.GitTimeout = t.TempDir(), time.Second
	if got := localScanner("", cfg).ConfigHash(); got != hash {
		t.Fatalf("pattern order or runtime settings changed hash: %q != %q", got, hash)
	}
}

func baseFromScan(scan *IncrementalScanResult) *IncrementalBase {
	base := &IncrementalBase{
		ScannerConfigHash: scan.ScannerConfigHash, CommitHash: scan.Commit.Hash, TreeHash: scan.Commit.TreeHash,
		Files: make(map[string]PreviousFile, len(scan.Files)),
	}
	for _, file := range scan.Files {
		base.Files[file.Path] = PreviousFile{
			Path: file.Path, BlobOID: file.BlobOID, Size: file.Size, Mode: file.Mode, SHA256: file.SHA256, LineCount: file.LineCount,
		}
	}
	return base
}

func TestIncrementalUnchangedWithoutTree(t *testing.T) {
	dir := newFixture(t)
	writeFixture(t, dir, "a", "first", 0644)
	fixtureGit(t, dir, "add", ".")
	saveFixture(t, dir)
	cfg := config.DefaultScannerConfig()
	spec := SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: dir}
	scanner := localScanner(dir, cfg)
	first, err := scanner.ScanIncremental(t.Context(), spec, nil, ScanOptions{})
	if err != nil || first.BlobsRead != 1 || first.AddedFiles != 1 {
		t.Fatalf("initial scan: %+v, %v", first, err)
	}
	// Reject tree and blob reads while keeping commit resolution available.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	writeFixture(t, shim, "git", `#!/bin/sh
for arg do
	case "$arg" in ls-tree|--batch) exit 97 ;; esac
done
exec "$PROJECTLENS_TEST_GIT" "$@"
`, 0755)
	t.Setenv("PROJECTLENS_TEST_GIT", git)
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	base := baseFromScan(first)
	same, err := scanner.ScanIncremental(t.Context(), spec, base, ScanOptions{})
	if err != nil || !same.Unchanged || same.BlobsRead != 0 || same.UnchangedFiles != 1 {
		t.Fatalf("unchanged scan: %+v, %v", same, err)
	}
	cfg.MaxFiles--
	if _, err := localScanner(dir, cfg).ScanIncremental(t.Context(), spec, base, ScanOptions{}); err == nil {
		t.Fatal("changed config did not require the tree")
	}
}

func TestIncrementalReadAndReuse(t *testing.T) {
	dir := newFixture(t)
	for name, content := range map[string]string{
		"mode": "mode\n", "old": "rename\nme", "deleted": "gone", "modified": "before", "unchanged": "as was",
		"duplicate": "rename\nme", "replaced": "will have a different existing blob",
	} {
		writeFixture(t, dir, name, content, 0644)
	}
	fixtureGit(t, dir, "add", ".")
	firstOID := saveFixture(t, dir)
	cfg := config.DefaultScannerConfig()
	spec := SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: dir}
	scanner := localScanner(dir, cfg)
	options := ScanOptions{IncludeContent: true}
	first, err := scanner.ScanIncremental(t.Context(), spec, nil, options)
	if err != nil || first.BlobsRead != 7 {
		t.Fatalf("initial scan: %+v, %v", first, err)
	}
	fixtureGit(t, dir, "mv", "old", "renamed")
	fixtureGit(t, dir, "rm", "deleted")
	writeFixture(t, dir, "modified", "after", 0644)
	writeFixture(t, dir, "added", "new content", 0644)
	writeFixture(t, dir, "replaced", "rename\nme", 0644)
	writeFixture(t, dir, "nul", strings.Repeat("a", 9000)+"\x00", 0644)
	writeFixture(t, dir, "invalid", strings.Repeat("a", 9000)+"\xff", 0644)
	fixtureGit(t, dir, "add", ".")
	fixtureGit(t, dir, "update-index", "--chmod=+x", "mode")
	saveFixture(t, dir, firstOID)
	base := baseFromScan(first)
	got, err := scanner.ScanIncremental(t.Context(), spec, base, options)
	if err != nil || got.Unchanged || got.AddedFiles != 2 || got.ModifiedFiles != 3 || got.DeletedFiles != 2 ||
		got.UnchangedFiles != 2 || got.BlobsRead != 4 || got.Stats.SkippedByReason[string(SkipBinaryFile)] != 2 ||
		!reflect.DeepEqual(got.DeletedPaths, []string{"deleted", "old"}) {
		t.Fatalf("incremental scan: %+v, %v", got, err)
	}
	for _, file := range got.Files {
		switch file.Path {
		case "renamed", "replaced":
			prior := base.Files["duplicate"]
			if !file.Reused || file.ReusedFromPath != prior.Path || file.SHA256 != prior.SHA256 || file.LineCount != 2 || file.Content != nil {
				t.Errorf("cross-path reuse: %+v", file)
			}
		case "mode", "duplicate", "unchanged":
			if !file.Reused || file.ReusedFromPath != file.Path || file.Content != nil || file.IsExecutable != (file.Path == "mode") {
				t.Errorf("same-path reuse: %+v", file)
			}
		case "modified":
			if file.Reused || string(file.Content) != "after" {
				t.Errorf("new content: %+v", file)
			}
		case "added":
			if file.Reused || string(file.Content) != "new content" {
				t.Errorf("added content: %+v", file)
			}
		default:
			t.Errorf("unexpected included file: %+v", file)
		}
	}
	cfg.ExcludePatterns = []string{"added"}
	reconfigured, err := localScanner(dir, cfg).ScanIncremental(t.Context(), spec, baseFromScan(got), options)
	if err != nil || reconfigured.Unchanged || reconfigured.ScannerConfigHash == got.ScannerConfigHash ||
		reconfigured.BlobsRead != 2 || !reflect.DeepEqual(reconfigured.DeletedPaths, []string{"added"}) {
		t.Fatalf("config rescan: %+v, %v", reconfigured, err)
	}
}

func TestGitRunnerCommandBoundary(t *testing.T) {
	cache, outside := t.TempDir(), t.TempDir()
	dir := filepath.Join(cache, "managed.git")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(cache, "escape.git")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(cache, "dangling.git")
	if err := os.Symlink(filepath.Join(outside, "missing.git"), dangling); err != nil {
		t.Fatal(err)
	}
	runner := gitRunner{cacheDir: cache}
	gitDir := "--git-dir=" + dir
	var denied [][]string
	for _, command := range []string{"push", "commit", "reset", "checkout", "clean", "rm", "update-ref", "gc", "archive", "hash-object"} {
		denied = append(denied, []string{gitDir, command})
	}
	for _, path := range []string{cache, outside, escape, filepath.Join(escape, "child"), dangling} {
		denied = append(denied,
			[]string{"init", "--bare", "--template=", path},
			[]string{"--git-dir=" + path, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", "--", "https://github.com/owner/project.git", "HEAD"})
	}
	denied = append(denied,
		[]string{"-c", "alias.inspection=!touch sentinel", gitDir, "inspection"},
		[]string{"init", "--bare", "--template=" + outside, dir},
		[]string{gitDir, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", "--", "file:///source.git", "HEAD"},
		[]string{gitDir, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", "--", "https://github.com/owner/project.git", "HEAD:refs/heads/main"},
		[]string{gitDir, "cat-file", "--filters", "HEAD:a.txt"},
		[]string{gitDir, "diff", "--output=" + filepath.Join(outside, "output")})
	for _, args := range denied {
		if _, err := runner.run(t.Context(), 1024, false, args...); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("run(%v) = %v, want forbidden", args, err)
		}
	}
	if _, err := (gitRunner{}).run(t.Context(), 1024, false, "init", "--bare", "--template=", dir); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("init without configured cache = %v", err)
	}
	if _, err := runner.run(t.Context(), 8192, false, "init", "--bare", "--template=", dir); err != nil {
		t.Fatalf("init within cache: %v", err)
	}
	fetch := []string{gitDir, "fetch", "--no-tags", "--prune", "--no-auto-maintenance", "--depth=1", "--", "https://github.com/owner/project.git", "HEAD"}
	if cmd := runner.command(t.Context(), true, fetch...); cmd.Err != nil {
		t.Fatalf("valid confined fetch: %v", cmd.Err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "refs/heads/escape")); err != nil {
		t.Fatal(err)
	}
	if cmd := runner.command(t.Context(), true, fetch...); !errors.Is(cmd.Err, domain.ErrForbidden) {
		t.Fatalf("fetch with an escaping cache ref = %v, want forbidden", cmd.Err)
	}
}

func TestLocalSourceRejectsEscapingAlternates(t *testing.T) {
	dir, outside := newFixture(t), newFixture(t)
	writeFixture(t, dir, ".git/objects/info/alternates", filepath.Join(outside, ".git/objects")+"\n", 0600)
	spec := SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: dir}
	if _, err := NewLocalSource([]string{dir}).Prepare(t.Context(), spec); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Prepare = %v, want forbidden", err)
	}
}

func TestLocalScannerIgnoresRepositoryScripts(t *testing.T) {
	dir := newFixture(t)
	writeFixture(t, dir, ".gitattributes", "*.txt filter=sentinel diff=sentinel\n", 0600)
	writeFixture(t, dir, "tracked.txt", "before\n", 0600)
	fixtureGit(t, dir, "add", ".")
	base := saveFixture(t, dir)
	writeFixture(t, dir, "tracked.txt", "after\n", 0600)
	fixtureGit(t, dir, "add", ".")
	head := saveFixture(t, dir, base)
	blob := fixtureGit(t, dir, "rev-parse", "HEAD:tracked.txt")

	sentinel := filepath.Join(t.TempDir(), "invoked")
	t.Setenv("PROJECTLENS_TEST_SENTINEL", sentinel)
	script := "#!/bin/sh\nprintf invoked >> \"$PROJECTLENS_TEST_SENTINEL\"\n"
	writeFixture(t, dir, "sentinel.sh", script, 0700)
	writeFixture(t, dir, "hooks/reference-transaction", script, 0700)
	writeFixture(t, dir, "hooks/post-index-change", script, 0700)
	program := filepath.Join(dir, "sentinel.sh")
	for key, value := range map[string]string{
		"core.fsmonitor": program, "core.hooksPath": filepath.Join(dir, "hooks"),
		"core.alternateRefsCommand": program, "diff.external": program, "diff.textconv": program,
		"diff.sentinel.command": program, "diff.sentinel.textconv": program,
		"filter.sentinel.clean": program, "filter.sentinel.smudge": program, "filter.sentinel.process": program,
	} {
		fixtureGit(t, dir, "config", key, value)
	}
	type entry struct {
		mode os.FileMode
		hash [sha256.Size]byte
	}
	snapshot := func() map[string]entry {
		t.Helper()
		files := make(map[string]entry)
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			item := entry{mode: info.Mode()}
			if !d.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				item.hash = sha256.Sum256(data)
			}
			files[path] = item
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}

	before := snapshot()
	scanner := localScanner(dir, config.DefaultScannerConfig())
	spec := SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: dir}
	if scan, err := scanner.Scan(t.Context(), spec, ScanOptions{}); err != nil || scan.Commit.Hash != head {
		t.Fatalf("Scan = %+v, %v", scan, err)
	}
	if scan, err := scanner.ScanIncremental(t.Context(), spec, nil, ScanOptions{}); err != nil || scan.Commit.Hash != head {
		t.Fatalf("ScanIncremental = %+v, %v", scan, err)
	}
	missing := func(ids []string) (map[string]bool, error) { return map[string]bool{base: true, head: true}, nil }
	save := func(HistoryCommit, Changes) error { return nil }
	if page, err := scanner.History(t.Context(), spec, 2, missing, save); err != nil || page.Imported != 2 {
		t.Fatalf("History = %+v, %v", page, err)
	}
	if changes, err := scanner.Compare(t.Context(), spec, base, head); err != nil || !strings.Contains(changes.Patch, "+after") {
		t.Fatalf("Compare = %+v, %v", changes, err)
	}
	if data, err := scanner.ReadBlob(t.Context(), spec, head, blob); err != nil || string(data) != "after\n" {
		t.Fatalf("ReadBlob = %q, %v", data, err)
	}
	if _, err := os.Lstat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository script ran: %v", err)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("scanner changed source files or Git metadata")
	}
}
