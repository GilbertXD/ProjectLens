package gitrepo

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"projectlens/internal/domain"
)

func git(t *testing.T, path string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func link(t *testing.T, target, path string) string {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalInspector(t *testing.T) {
	parent := t.TempDir()
	root := mkdir(t, filepath.Join(parent, "allowed"))
	repo := mkdir(t, filepath.Join(root, "sample"))
	git(t, repo, "init", "-b", "topic")
	git(t, repo, "remote", "add", "origin", "https://user:secret@example.com/team/repo.git")
	canonical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	alias := link(t, "sample", filepath.Join(root, "alias"))
	rootAlias := link(t, root, filepath.Join(parent, "linked-root"))
	for _, tc := range []struct{ root, path string }{
		{root, repo}, {root, alias}, {rootAlias, filepath.Join(rootAlias, "sample")}, {rootAlias, canonical},
	} {
		got, err := NewLocalInspector([]string{tc.root}).Inspect(t.Context(), tc.path)
		if err != nil || got != (Metadata{Name: "sample", CanonicalSource: canonical, DefaultBranch: "topic", RemoteURL: "https://example.com/team/repo.git"}) {
			t.Fatalf("inspect %q = %+v, %v", tc.path, got, err)
		}
	}
	nested := mkdir(t, filepath.Join(repo, "nested"))
	plain := mkdir(t, filepath.Join(root, "plain"))
	outside := mkdir(t, filepath.Join(parent, "outside"))
	escape := link(t, outside, filepath.Join(root, "escape"))
	dangling := link(t, filepath.Join(outside, "missing"), filepath.Join(root, "dangling"))
	for _, tc := range []struct {
		name, path string
		want       error
	}{
		{"plain directory", plain, domain.ErrInvalidInput}, {"subdirectory", nested, domain.ErrInvalidInput},
		{"missing", filepath.Join(root, "missing"), domain.ErrInvalidInput}, {"relative", "sample", domain.ErrInvalidInput},
		{"blank", "  ", domain.ErrInvalidInput}, {"NUL", repo + "\x00", domain.ErrInvalidInput},
		{"outside", outside, domain.ErrForbidden}, {"outside missing", filepath.Join(outside, "missing"), domain.ErrForbidden},
		{"traversal", root + "/../outside", domain.ErrForbidden}, {"escape", escape, domain.ErrForbidden},
		{"dangling escape", dangling, domain.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewLocalInspector([]string{root}).Inspect(t.Context(), tc.path); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := NewLocalInspector(nil).Inspect(t.Context(), repo); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("empty roots: %v", err)
	}
}

func TestLocalInspectorPermissions(t *testing.T) {
	for _, suffix := range []string{".", ".git"} {
		t.Run(suffix, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init")
			path := filepath.Join(repo, suffix)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Chmod(path, info.Mode().Perm()); err != nil {
					t.Error(err)
				}
			})
			if os.Geteuid() == 0 {
				t.Skip("root can access directories with mode 000")
			}
			if _, err := NewLocalInspector([]string{repo}).Inspect(t.Context(), repo); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("got %v, want forbidden", err)
			}
		})
	}
}

func TestLocalInspectorDotGit(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	repo := mkdir(t, filepath.Join(root, "repo"))
	inspector := NewLocalInspector([]string{root})
	if _, err := inspector.Inspect(t.Context(), repo); !errors.Is(err, domain.ErrInvalidInput) || !strings.Contains(err.Error(), "directory is not a Git repository") {
		t.Fatalf("missing .git: %v", err)
	}
	git(t, outside, "init")
	link(t, filepath.Join(outside, ".git"), filepath.Join(repo, ".git"))
	if _, err := inspector.Inspect(t.Context(), repo); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("escaping .git symlink: %v", err)
	}
}
