package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"projectlens/internal/domain"
	"projectlens/internal/service"
)

type syncTxObserver func(context.Context, func(context.Context) error) error

func (observe syncTxObserver) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	return observe(ctx, fn)
}

func TestHistoryReachabilityAndChanges(t *testing.T) {
	f := localSyncFixture(t)
	ctx := t.Context()
	f.write(t, "one", "line 1\n")
	first := f.commit(t, "first")
	syncGit(t, f.dir, "checkout", "-b", "side")
	f.write(t, "side", "side")
	side := f.commit(t, "side")
	syncGit(t, f.dir, "checkout", "main")
	syncGit(t, f.dir, "mv", "one", "two")
	f.write(t, "binary", "hello\x00world")
	rename := f.commit(t, "rename")
	syncGit(t, f.dir, "merge", "--no-ff", "-m", "merge", "side")
	shim := t.TempDir()
	marker := filepath.Join(shim, "transaction-active")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ -e "$PROJECTLENS_TEST_HISTORY_TX" ]; then
	echo "Git invoked inside the history transaction" >&2
	exit 1
fi
exec "$PROJECTLENS_TEST_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROJECTLENS_TEST_GIT", git)
	t.Setenv("PROJECTLENS_TEST_HISTORY_TX", marker)
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	tx := syncTxObserver(func(ctx context.Context, fn func(context.Context) error) error {
		return f.tx.WithinTx(ctx, func(ctx context.Context) error {
			if err := os.WriteFile(marker, nil, 0600); err != nil {
				return err
			}
			defer func() {
				if err := os.Remove(marker); err != nil {
					t.Error(err)
				}
			}()
			return fn(ctx)
		})
	})
	h := service.HistoryService{Repositories: f.repos, Locks: f.store, Scanner: f.scanner, Tx: tx}
	target := domain.SyncTarget{ProjectID: f.projectID, RepositoryID: f.repoID, Ref: "main"}
	syncCount := func(n int) {
		t.Helper()
		result, err := h.Sync(ctx, target)
		if err != nil || result.Imported != n {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	// A publish failure must also roll back the immutable objects inserted in that batch.
	if _, err := f.pool.Exec(ctx, "ALTER TABLE repository_history_refs ADD CONSTRAINT injected_failure CHECK (false)"); err != nil {
		t.Fatal(err)
	}
	var publicationErr *pgconn.PgError
	if _, err := h.Sync(ctx, target); !errors.As(err, &publicationErr) || publicationErr.ConstraintName != "injected_failure" {
		t.Fatalf("expected injected publication failure after scanning Git, got %v", err)
	}
	var leaked int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM repository_commits").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal(leaked, err)
	}
	if _, err := f.pool.Exec(ctx, "ALTER TABLE repository_history_refs DROP CONSTRAINT injected_failure"); err != nil {
		t.Fatal(err)
	}
	syncCount(4)
	syncCount(0)
	read := func(op, sha, base string) string {
		t.Helper()
		v, err := h.Read(ctx, target, op, sha, base, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		buf, _ := json.Marshal(v)
		return string(buf)
	}
	if v := read("files", rename, ""); !strings.Contains(v, "old_path") || !strings.Contains(v, "binary\":true") {
		t.Fatal(v)
	}
	if v := read("compare", rename, first); !strings.Contains(v, "two") {
		t.Fatal(v)
	}
	if v := read("list", "", ""); !strings.Contains(v, side) {
		t.Fatal(v)
	}
	syncGit(t, f.dir, "reset", "--hard", first)
	f.write(t, "one", "changed")
	f.commit(t, "rewrite")
	syncCount(1)
	if v := read("list", "", ""); strings.Contains(v, side) {
		t.Fatal(v)
	}
	if v := read("detail", side, ""); !strings.Contains(v, fmt.Sprintf("%q", side)) {
		t.Fatal(v)
	}

	syncGit(t, f.dir, "rm", "one")
	for i := range 501 {
		f.write(t, fmt.Sprintf("large/%d", i), strings.Repeat("diff line\n", 40))
	}
	large := f.commit(t, "large diff")
	syncCount(1)
	var compared struct {
		Truncated      bool
		Patch          string
		PatchTruncated bool `json:"patch_truncated"`
	}
	if err := json.Unmarshal([]byte(read("compare", large, first)), &compared); err != nil || !compared.Truncated || !compared.PatchTruncated || len(compared.Patch) > 64<<10 {
		t.Fatal("compare did not cap patch", err)
	}
	if v := read("files", large, ""); !strings.Contains(v, `"truncated":true`) {
		t.Fatal("missing file truncation")
	}
}

func TestHistoryPages(t *testing.T) {
	f := localSyncFixture(t)
	var stream strings.Builder
	for i := 1; i <= 102; i++ {
		fmt.Fprintf(&stream, "commit refs/heads/main\nmark :%d\ncommitter Fixture <test@example.org> %d +0000\ndata 1\nx\n", i, 1700000000+i)
		if i > 1 {
			fmt.Fprintf(&stream, "from :%d\n", i-1)
		}
		stream.WriteByte('\n')
	}
	cmd := exec.CommandContext(t.Context(), "git", "-C", f.dir, "fast-import", "--quiet")
	cmd.Stdin = strings.NewReader(stream.String())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	svc := service.HistoryService{Repositories: f.repos, Locks: f.store, Scanner: f.scanner, Tx: f.tx}
	target := domain.SyncTarget{RepositoryID: f.repoID, ProjectID: f.projectID, Ref: "main"}
	for i, n := range []int{100, 2, 0} {
		page, err := svc.Sync(t.Context(), target)
		if err != nil || page.Imported != n || page.HasMore != (i == 0) {
			t.Fatal(page, err)
		}
	}
	result, err := f.repos.HistoryList(t.Context(), f.repoID, "main", 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []json.RawMessage
		Total int
	}
	if err := json.Unmarshal(result, &page); err != nil || page.Total != 102 || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
}
