package gitscan

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

func TestLiveLocalScanner(t *testing.T) {
	dir := os.Getenv("TEST_SCAN_LOCAL_REPO")
	if dir == "" {
		t.Skip("TEST_SCAN_LOCAL_REPO is unset")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	beforeStatus := fixtureGit(t, absolute, "status", "--porcelain")
	beforeHead := fixtureGit(t, absolute, "rev-parse", "HEAD")
	t.Cleanup(func() {
		if fixtureGit(t, absolute, "status", "--porcelain") != beforeStatus || fixtureGit(t, absolute, "rev-parse", "HEAD") != beforeHead {
			t.Error("local repository changed during scan")
		}
	})
	scanner := localScanner(absolute, config.DefaultScannerConfig())
	assertLiveScan(t, scanner, SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: absolute}, beforeHead)
}

func TestLiveGitHubScanner(t *testing.T) {
	url := os.Getenv("TEST_SCAN_REMOTE_REPO")
	if url == "" {
		t.Skip("TEST_SCAN_REMOTE_REPO is unset")
	}
	cfg := config.DefaultScannerConfig()
	cfg.CacheDir = t.TempDir()
	scanner := NewScanner(cfg, config.RepositoryConfig{}, config.GitHubConfig{Token: os.Getenv("GITHUB_TOKEN")})
	spec := SourceSpec{SourceType: domain.RepositorySourceGitHub, Source: url}
	cold := assertLiveScan(t, scanner, spec, "")
	warm := assertLiveScan(t, scanner, spec, cold.Commit.Hash)
	if !reflect.DeepEqual(cold.Commit, warm.Commit) ||
		!reflect.DeepEqual(cold.Files, warm.Files) ||
		!reflect.DeepEqual(cold.Skipped, warm.Skipped) ||
		!reflect.DeepEqual(cold.Stats, warm.Stats) {
		t.Fatal("warm-cache manifest differs from cold-cache manifest")
	}
}

func assertLiveScan(t *testing.T, scanner *Scanner, spec SourceSpec, wantCommit string) *Snapshot {
	t.Helper()
	snapshot, err := scanner.Scan(t.Context(), spec, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !validOID(snapshot.Commit.Hash) || (wantCommit != "" && snapshot.Commit.Hash != wantCommit) ||
		len(snapshot.Files) == 0 || !sort.SliceIsSorted(snapshot.Files, func(i, j int) bool {
		return snapshot.Files[i].Path < snapshot.Files[j].Path
	}) {
		t.Fatalf("invalid manifest for commit %q with %d files", snapshot.Commit.Hash, len(snapshot.Files))
	}
	return snapshot
}
