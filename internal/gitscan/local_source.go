package gitscan

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

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
	if spec.requireCommit && !validOID(ref) {
		return nil, fmt.Errorf("%w: expected a full commit SHA", domain.ErrInvalidInput)
	}
	meta, err := s.inspector.Inspect(ctx, spec.Source)
	if err != nil {
		return nil, err
	}
	git := gitRunner{}
	oid, err := git.resolve(ctx, meta.GitDir, ref)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if spec.requireCommit || spec.deepenHistory && validOID(ref) {
			return nil, fmt.Errorf("%w: commit not found in repository", domain.ErrNotFound)
		}
		if ref == "HEAD" {
			return nil, fmt.Errorf("%w: local repository HEAD has no commit", domain.ErrInvalidInput)
		}
		return nil, fmt.Errorf("%w: Git ref does not resolve to a commit", domain.ErrInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	if validOID(ref) && (spec.requireCommit || spec.preferCache) && oid != ref {
		return nil, fmt.Errorf("%w: commit not found in repository", domain.ErrNotFound)
	}
	shallow, err := git.isShallow(ctx, meta.GitDir)
	if err != nil {
		return nil, err
	}
	return &PreparedRepo{
		SourceType: domain.RepositorySourceLocalGit, Source: spec.Source, CanonicalSource: meta.CanonicalSource,
		GitDir: meta.GitDir, CommonDir: meta.CommonDir, ResolvedRef: ref, PinnedCommit: oid, Shallow: shallow, Release: func() {},
	}, nil
}

func (s *LocalSource) ListRefs(ctx context.Context, spec SourceSpec) ([]GitRef, string, error) {
	meta, err := s.inspector.Inspect(ctx, spec.Source)
	if err != nil {
		return nil, "", err
	}
	git := gitRunner{}
	dir := "--git-dir=" + meta.GitDir
	raw, err := git.run(ctx, refOutputLimit, false, dir, "for-each-ref", refFormat, "refs/heads", "refs/tags")
	if err != nil {
		return nil, "", err
	}
	head, err := git.run(ctx, 8192, false, dir, "symbolic-ref", "--quiet", "HEAD")
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return nil, "", err
	}
	defaultBranch, _ := strings.CutPrefix(strings.TrimSuffix(string(head), "\n"), "refs/heads/")
	items := []GitRef{}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\x00")
		if len(fields) != 4 || !validOID(fields[1]) || fields[2] != "" && !validOID(fields[2]) {
			return nil, "", errors.New("invalid Git ref record")
		}
		item := makeGitRef(fields[0], fields[1])
		switch fields[3] {
		case "commit":
			item.CommitHash = item.ObjectHash
		case "tag":
			item.CommitHash, err = git.resolve(ctx, meta.GitDir, item.ObjectHash)
			if err != nil && !errors.As(err, &exit) {
				return nil, "", err
			}
		}
		items = append(items, item)
	}
	return items, defaultBranch, nil
}
