package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"projectlens/internal/domain"
)

type Metadata struct {
	Name            string
	CanonicalSource string
	DefaultBranch   string
	RemoteURL       string
}

type LocalInspector struct {
	roots []string
}

func NewLocalInspector(allowedRoots []string) *LocalInspector {
	return &LocalInspector{roots: append([]string(nil), allowedRoots...)}
}

type localRoot struct {
	configured string
	resolved   string
}

func inside(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	return rel, err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func pathError(err error) error {
	switch {
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w: local repository path is unreadable", domain.ErrForbidden)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: local repository path does not exist", domain.ErrInvalidInput)
	default:
		return fmt.Errorf("%w: cannot inspect local repository path", domain.ErrInvalidInput)
	}
}

func outsideRoots() error {
	return fmt.Errorf("%w: local repository path is outside configured roots", domain.ErrForbidden)
}

// Check each link destination before following it, including dangling links.
func resolveWithin(path string, roots []localRoot) (string, error) {
	for links := 0; links <= 255; links++ {
		var base, relative string
		for _, root := range roots {
			for _, prefix := range []string{root.configured, root.resolved} {
				if rel, ok := inside(prefix, path); ok {
					base, relative = root.resolved, rel
					break
				}
			}
			if base != "" {
				break
			}
		}
		if base == "" {
			return "", outsideRoots()
		}
		if relative == "." {
			return base, nil
		}
		parts := strings.Split(relative, string(filepath.Separator))
		followed := false
		for i, part := range parts {
			base = filepath.Join(base, part)
			info, err := os.Lstat(base)
			if err != nil {
				return "", pathError(err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			target, err := os.Readlink(base)
			if err != nil {
				return "", pathError(err)
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(base), target)
			}
			path = filepath.Join(append([]string{target}, parts[i+1:]...)...)
			followed = true
			break
		}
		if !followed {
			return base, nil
		}
	}
	return "", fmt.Errorf("%w: too many symlinks", domain.ErrInvalidInput)
}

func (i *LocalInspector) Inspect(ctx context.Context, rawPath string) (Metadata, error) {
	if len(i.roots) == 0 {
		return Metadata{}, outsideRoots()
	}
	path := strings.TrimSpace(rawPath)
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return Metadata{}, fmt.Errorf("%w: source must be an absolute local path without NUL", domain.ErrInvalidInput)
	}
	path = filepath.Clean(path)
	var roots []localRoot
	for _, rawRoot := range i.roots {
		root := filepath.Clean(rawRoot)
		if !filepath.IsAbs(root) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			if _, ok := inside(root, path); ok && errors.Is(err, os.ErrPermission) {
				return Metadata{}, pathError(err)
			}
			continue
		}
		roots = append(roots, localRoot{configured: root, resolved: resolved})
	}
	canonical, err := resolveWithin(path, roots)
	if err != nil {
		return Metadata{}, err
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		return Metadata{}, pathError(err)
	}
	allowed := false
	for _, root := range roots {
		if _, ok := inside(root.resolved, canonical); ok {
			allowed = true
			break
		}
	}
	if !allowed {
		return Metadata{}, outsideRoots()
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return Metadata{}, pathError(err)
	}
	if !info.IsDir() {
		return Metadata{}, fmt.Errorf("%w: source is not a directory", domain.ErrInvalidInput)
	}
	if err := checkReadable(canonical); err != nil {
		return Metadata{}, err
	}
	gitPath := filepath.Join(canonical, ".git")
	if _, err := os.Lstat(gitPath); errors.Is(err, os.ErrNotExist) {
		return Metadata{}, fmt.Errorf("%w: directory is not a Git repository", domain.ErrInvalidInput)
	} else if err != nil {
		return Metadata{}, pathError(err)
	}
	gitPath, err = resolveWithin(gitPath, roots)
	if err != nil {
		return Metadata{}, err
	}
	if err := checkReadable(gitPath); err != nil {
		return Metadata{}, err
	}

	// Reading directory entries alone does not require search permission.
	if _, err := os.Stat(filepath.Join(gitPath, "HEAD")); errors.Is(err, os.ErrPermission) {
		return Metadata{}, pathError(err)
	}

	output, err := runGit(ctx, canonical, "rev-parse", "--is-inside-work-tree", "--show-toplevel")
	if err != nil {
		return Metadata{}, err
	}
	insideTree, top, _ := strings.Cut(output, "\n")
	if insideTree != "true" {
		return Metadata{}, fmt.Errorf("%w: source is not a Git work tree", domain.ErrInvalidInput)
	}
	if !filepath.IsAbs(top) {
		return Metadata{}, fmt.Errorf("%w: Git did not return an absolute work tree path", domain.ErrInvalidInput)
	}
	top, err = resolveWithin(filepath.Clean(top), roots)
	if err != nil {
		return Metadata{}, err
	}
	if top != canonical {
		return Metadata{}, fmt.Errorf("%w: source must be the root of the Git work tree", domain.ErrInvalidInput)
	}

	branch, err := runGit(ctx, canonical, "symbolic-ref", "--short", "HEAD")
	if errors.Is(err, domain.ErrInvalidInput) {
		branch, err = runGit(ctx, canonical, "rev-parse", "--abbrev-ref", "HEAD")
	}
	if err != nil && !errors.Is(err, domain.ErrInvalidInput) {
		return Metadata{}, err
	}
	remote, err := runGit(ctx, canonical, "config", "--local", "--no-includes", "--get", "remote.origin.url")
	if err != nil && !errors.Is(err, domain.ErrInvalidInput) {
		return Metadata{}, err
	}
	if target, err := url.Parse(remote); err == nil && (target.Scheme == "http" || target.Scheme == "https") && target.User != nil {
		target.User = nil
		remote = target.String()
	}
	return Metadata{Name: filepath.Base(canonical), CanonicalSource: canonical, DefaultBranch: branch, RemoteURL: remote}, nil
}

func checkReadable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return pathError(err)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("%w: expected a directory or Git file", domain.ErrInvalidInput)
	}
	file, err := os.Open(path)
	if err != nil {
		return pathError(err)
	}
	defer file.Close()
	if info.IsDir() {
		_, err = file.Readdirnames(1)
	} else {
		var sample [1]byte
		_, err = file.Read(sample[:])
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return pathError(err)
	}
	return nil
}

func runGit(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "LC_ALL=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(path), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err == nil {
		return strings.TrimSuffix(string(output), "\n"), nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if errors.Is(err, os.ErrPermission) || strings.Contains(stderr.String(), "Permission denied") || strings.Contains(stderr.String(), "Operation not permitted") {
		return "", fmt.Errorf("%w: Git cannot read the local repository", domain.ErrForbidden)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return "", fmt.Errorf("run local Git: %w", err)
	}
	return "", fmt.Errorf("%w: Git could not inspect the work tree", domain.ErrInvalidInput)
}
