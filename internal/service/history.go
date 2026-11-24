package service

import (
	"context"
	"fmt"

	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/gitscan"
	"projectlens/internal/repository"
)

type HistoryService struct {
	Repositories *repository.PostgresRepositoryStore
	Locks        *repository.PostgresSnapshotStore
	Scanner      *gitscan.Scanner
	Tx           db.TxManager
}

func sourceSpec(repo domain.Repository, ref string) gitscan.SourceSpec {
	source := repo.Source
	if repo.SourceType == domain.RepositorySourceLocalGit && repo.LocalPath != nil {
		source = *repo.LocalPath
	}
	if repo.SourceType == domain.RepositorySourceGitHub && repo.RemoteURL != "" {
		source = repo.RemoteURL
	}
	return gitscan.SourceSpec{SourceType: repo.SourceType, Source: source, Ref: ref}
}
func (s *HistoryService) Sync(ctx context.Context, target domain.SyncTarget) (gitscan.HistoryPageResult, error) {
	var page gitscan.HistoryPageResult
	release, err := s.Locks.AcquireScopedSyncLock(ctx, "commits", target.RepositoryID)
	if err != nil {
		return page, err
	}
	defer release()
	repo, err := s.Repositories.GetByID(ctx, target.ProjectID, target.RepositoryID)
	if err != nil {
		return page, err
	}
	ref, err := gitscan.NormalizeRef(target.Ref)
	if err != nil {
		return page, err
	}
	size, err := s.Repositories.HistorySize(ctx, repo.ID, ref)
	if err != nil {
		return page, err
	}
	type stagedCommit struct {
		item    gitscan.HistoryCommit
		changes gitscan.Changes
	}
	var commits []stagedCommit
	page, err = s.Scanner.History(ctx, sourceSpec(repo, ref), size+gitscan.HistoryPage, func(ids []string) (map[string]bool, error) {
		return s.Repositories.MissingCommits(ctx, repo.ID, ids)
	}, func(item gitscan.HistoryCommit, changes gitscan.Changes) error {
		commits = append(commits, stagedCommit{item, changes})
		return nil
	})
	if err != nil {
		return page, err
	}
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		for _, commit := range commits {
			if err := s.Repositories.StoreCommit(ctx, repo.ID, commit.item, commit.changes); err != nil {
				return err
			}
		}
		return s.Repositories.PublishHistory(ctx, repo.ID, ref, page)
	})
	return page, err
}
func (s *HistoryService) Read(ctx context.Context, target domain.SyncTarget, operation, sha, base string, limit, offset int) (any, error) {
	repo, err := s.Repositories.GetByID(ctx, target.ProjectID, target.RepositoryID)
	if err != nil {
		return nil, err
	}
	if operation == "list" {
		ref, err := gitscan.NormalizeRef(target.Ref)
		if err != nil {
			return nil, err
		}
		return s.Repositories.HistoryList(ctx, repo.ID, ref, limit, offset)
	}
	if !gitscan.ValidSHA(sha) {
		return nil, fmt.Errorf("%w: expected full SHA", domain.ErrInvalidInput)
	}
	if operation != "compare" {
		return s.Repositories.Commit(ctx, repo.ID, sha, operation == "files", limit, offset)
	}
	if !gitscan.ValidSHA(base) {
		return nil, fmt.Errorf("%w: expected full base SHA", domain.ErrInvalidInput)
	}
	for _, id := range []string{base, sha} {
		if _, err := s.Repositories.Commit(ctx, repo.ID, id, false, 0, 0); err != nil {
			return nil, err
		}
	}
	changes, err := s.Scanner.Compare(ctx, sourceSpec(repo, ""), base, sha)
	if err != nil {
		return nil, err
	}
	n := len(changes.Files)
	start := min(offset, n)
	return map[string]any{"patch": changes.Patch, "patch_truncated": changes.PatchTruncated, "base": base, "head": sha, "items": changes.Files[start:min(n, start+limit)], "total": n, "truncated": changes.Truncated}, nil
}
