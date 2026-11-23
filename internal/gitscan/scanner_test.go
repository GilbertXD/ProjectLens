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
