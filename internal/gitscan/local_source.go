package gitscan

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"projectlens/internal/domain"
	"projectlens/internal/gitrepo"
)

type LocalSource struct {
	inspector *gitrepo.LocalInspector
}

func NewLocalSource(allowedRoots []string) *LocalSource {
	return &LocalSource{inspector: gitrepo.NewLocalInspector(allowedRoots)}
}

func (s *LocalSource) Prepare(ctx context.Context, spec SourceSpec) (*PreparedRepo, error) {
	ref, err := NormalizeRef(spec.Ref)
	if err != nil {
		return nil, err
	}
	meta, err := s.inspector.Inspect(ctx, spec.Source)
	if err != nil {
		return nil, err
	}
	git := gitRunner{}
	oid, err := git.resolve(ctx, meta.GitDir, ref)
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
		GitDir: meta.GitDir, ResolvedRef: ref, PinnedCommit: oid, Release: func() {},
	}, nil
}
