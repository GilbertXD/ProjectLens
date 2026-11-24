package gitrepo

import (
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
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
		if err != nil || got != (Metadata{Name: "sample", CanonicalSource: canonical, DefaultBranch: "topic", RemoteURL: "https://example.com/team/repo.git", GitDir: filepath.Join(canonical, ".git"), CommonDir: filepath.Join(canonical, ".git")}) {
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

func TestLocalInspectorRejectsEscapingGitMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(repo, outside string) error
	}{
		{"gitdir pointer", func(repo, outside string) error {
			return os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: "+filepath.Join(outside, ".git")+"\n"), 0600)
		}},
		{"commondir pointer", func(repo, outside string) error {
			return os.WriteFile(filepath.Join(repo, ".git/commondir"), []byte(filepath.Join(outside, ".git")+"\n"), 0600)
		}},
		{"alternate object store", func(repo, outside string) error {
			return os.WriteFile(filepath.Join(repo, ".git/objects/info/alternates"), []byte("# alternate\n\n"+filepath.Join(outside, ".git/objects")+"\n"), 0600)
		}},
		{"config symlink", func(repo, outside string) error {
			config := filepath.Join(repo, ".git/config")
			if err := os.Remove(config); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(outside, ".git/config"), config)
		}},
		{"HEAD symlink", func(repo, outside string) error {
			head := filepath.Join(repo, ".git/HEAD")
			if err := os.Remove(head); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(outside, ".git/HEAD"), head)
		}},
		{"alternate via symlink and parent traversal", func(repo, outside string) error {
			if err := os.Symlink(filepath.Join(outside, ".git/objects"), filepath.Join(repo, ".git/objects/link")); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(repo, ".git/objects/info/alternates"), []byte("link/../objects\n"), 0600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			repo := mkdir(t, filepath.Join(root, "repo"))
			git(t, outside, "init", "--template=")
			if tc.name != "gitdir pointer" {
				git(t, repo, "init", "--template=")
			}
			if err := tc.setup(repo, outside); err != nil {
				t.Fatal(err)
			}
			if _, err := NewLocalInspector([]string{root}).Inspect(t.Context(), repo); !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("Inspect = %v, want forbidden", err)
			}
		})
	}
}

func TestLocalInspectorLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	repo := mkdir(t, filepath.Join(root, "primary"))
	git(t, repo, "init", "--template=", "-b", "main")
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.org", "commit", "--allow-empty", "-m", "fixture")
	linked := filepath.Join(root, "linked")
	git(t, repo, "worktree", "add", "-b", "topic", linked, "HEAD")
	inspector := NewLocalInspector([]string{root})
	got, err := inspector.Inspect(t.Context(), linked)
	if err != nil {
		t.Fatal(err)
	}
	primary, err := filepath.EvalSymlinks(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if got.GitDir != filepath.Join(primary, "worktrees", "linked") || got.CommonDir != primary || got.DefaultBranch != "topic" {
		t.Fatalf("linked worktree metadata = %+v", got)
	}
	outside := t.TempDir()
	git(t, outside, "init", "--template=")
	alternates := filepath.Join(primary, "objects/info/alternates")
	if err := os.WriteFile(alternates, []byte(filepath.Join(outside, ".git/objects")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspector.Inspect(t.Context(), linked); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("linked worktree with escaping alternate = %v", err)
	}
}

func TestLocalInspectorReadOnly(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "--template=", "-b", "main")
	sentinel := filepath.Join(t.TempDir(), "invoked")
	t.Setenv("PROJECTLENS_TEST_SENTINEL", sentinel)
	script := filepath.Join(repo, "sentinel.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf invoked >> \"$PROJECTLENS_TEST_SENTINEL\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hooks := mkdir(t, filepath.Join(repo, "hooks"))
	link(t, script, filepath.Join(hooks, "reference-transaction"))
	for key, value := range map[string]string{
		"core.fsmonitor": script, "core.hooksPath": hooks,
		"diff.external": script, "core.alternateRefsCommand": script,
	} {
		git(t, repo, "config", key, value)
	}

	type entry struct {
		mode os.FileMode
		hash [sha256.Size]byte
	}
	capture := func(readOnly bool) map[string]entry {
		t.Helper()
		files := make(map[string]entry)
		err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if readOnly && info.Mode()&os.ModeSymlink == 0 {
				original := info.Mode().Perm()
				mode := os.FileMode(0400)
				if d.IsDir() {
					mode = 0500
				} else if path == script {
					mode = 0500
				}
				if err := os.Chmod(path, mode); err != nil {
					return err
				}
				t.Cleanup(func() {
					if err := os.Chmod(path, original); err != nil {
						t.Error(err)
					}
				})
				info, err = os.Stat(path)
				if err != nil {
					return err
				}
			}
			item := entry{mode: info.Mode()}
			if info.Mode().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				item.hash = sha256.Sum256(data)
			} else if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				item.hash = sha256.Sum256([]byte(target))
			}
			files[path] = item
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}

	before := capture(true)
	got, err := NewLocalInspector([]string{repo}).Inspect(t.Context(), repo)
	if err != nil || got.DefaultBranch != "main" {
		t.Fatalf("inspect read-only repository = %+v, %v", got, err)
	}
	_, known, err := InspectRootReadOnly(repo)
	if err != nil || known != (runtime.GOOS == "darwin" || runtime.GOOS == "linux") {
		t.Fatalf("InspectRootReadOnly known = %t, err = %v", known, err)
	}
	if after := capture(false); !reflect.DeepEqual(before, after) {
		t.Fatal("inspection changed the source repository")
	}
	if _, err := os.Lstat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository script ran: %v", err)
	}
}

func TestInspectorCommandGate(t *testing.T) {
	for _, args := range [][]string{{"push"}, {"config", "--local", "remote.origin.url", "changed"}, {"symbolic-ref", "HEAD", "refs/heads/other"}} {
		if _, err := runGit(t.Context(), t.TempDir(), args...); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("runGit(%v) = %v, want forbidden", args, err)
		}
	}
}
