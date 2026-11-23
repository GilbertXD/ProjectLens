package gitscan

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"projectlens/internal/domain"
	"projectlens/internal/gitrepo"
)

type LocalSource struct {
	inspector *gitrepo.LocalInspector
	roots     []string
}

func NewLocalSource(allowedRoots []string) *LocalSource {
	return &LocalSource{
		inspector: gitrepo.NewLocalInspector(allowedRoots),
		roots:     append([]string(nil), allowedRoots...),
	}
}

func (s *LocalSource) allowedGitDir(raw []byte) (string, error) {
	dir := strings.TrimSuffix(string(raw), "\n")
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w: Git returned a relative repository directory", domain.ErrInvalidInput)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("%w: cannot resolve Git directory", domain.ErrInvalidInput)
	}
	for _, root := range s.roots {
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w: Git directory is outside configured roots", domain.ErrForbidden)
}

func (s *LocalSource) Prepare(ctx context.Context, spec SourceSpec) (*PreparedRepo, error) {
	ref, err := scanRef(spec.Ref)
	if err != nil {
		return nil, err
	}
	meta, err := s.inspector.Inspect(ctx, spec.Source)
	if err != nil {
		return nil, err
	}
	git := gitRunner{}
	args := []string{"-C", meta.CanonicalSource, "rev-parse", "--path-format=absolute"}
	rawDir, err := git.run(ctx, 64<<10, false, append(args, "--git-dir")...)
	if err != nil {
		return nil, err
	}
	dir, err := s.allowedGitDir(rawDir)
	if err != nil {
		return nil, err
	}
	commonDir, err := git.run(ctx, 64<<10, false, append(args, "--git-common-dir")...)
	if err != nil {
		return nil, err
	}
	if _, err := s.allowedGitDir(commonDir); err != nil {
		return nil, err
	}
	oid, err := git.resolve(ctx, dir, ref)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ref == "HEAD" {
			return nil, fmt.Errorf("%w: local repository HEAD has no commit", domain.ErrInvalidInput)
		}
		return nil, fmt.Errorf("%w: Git ref does not resolve to a commit", domain.ErrInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	return &PreparedRepo{
		SourceType: domain.RepositorySourceLocalGit, Source: spec.Source, CanonicalSource: meta.CanonicalSource,
		GitDir: dir, ResolvedRef: ref, PinnedCommit: oid, Release: func() {},
	}, nil
}
