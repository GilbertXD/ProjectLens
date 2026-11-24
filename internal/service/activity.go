package service

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/github"
	"projectlens/internal/gitscan"
	"projectlens/internal/repository"
)

type ActivityService struct {
	Repositories *repository.PostgresRepositoryStore
	Locks        *repository.PostgresSnapshotStore
	Client       *github.Client
	Tx           db.TxManager
}

func (s *ActivityService) Sync(ctx context.Context, target domain.SyncTarget) (int, error) {
	id := target.RepositoryID
	unlock, err := s.Locks.AcquireRepoSyncLock(ctx, id)
	if err != nil {
		return 0, err
	}
	defer unlock()
	repo, err := s.Repositories.GetByID(ctx, target.ProjectID, id)
	if err != nil {
		return 0, err
	}
	if repo.SourceType != domain.RepositorySourceGitHub {
		return 0, fmt.Errorf("%w: expected GitHub repository", domain.ErrInvalidInput)
	}
	owner, name := *repo.GitHubOwner, *repo.GitHubRepo
	meta, err := s.Client.PublicActivityMetadata(ctx, owner, name)
	if err != nil {
		return 0, err
	}
	if repo.GitHubRepoID == nil || meta.ID != *repo.GitHubRepoID {
		return 0, fmt.Errorf("%w: repository identity changed", domain.ErrConflict)
	}
	feed, err := s.Client.Feed(owner, name)
	if err != nil {
		return 0, err
	}
	cursor, full, err := s.Repositories.ActivityCursor(ctx, id)
	if err != nil {
		return 0, err
	}
	start := time.Now().UTC()
	reconcile := start.Sub(full) >= 24*time.Hour
	query := url.Values{}
	if !reconcile && !cursor.IsZero() {
		query.Set("since", cursor.Add(-2*time.Minute).Format(time.RFC3339))
	}
	batch := uuid.New()
	saved := 0
	err = s.Tx.WithinTx(ctx, func(ctx context.Context) error {
		save := func(kind string, n int, a github.Activity, etag string) error {
			key := strconv.FormatInt(a.ID, 10)
			if kind == "commit" {
				key = a.SHA
				if !gitscan.ValidSHA(key) {
					return fmt.Errorf("%w: invalid associated SHA", domain.ErrInvalidInput)
				}
			} else if a.ID <= 0 {
				return fmt.Errorf("%w: missing GitHub ID", domain.ErrInvalidInput)
			}
			if n <= 0 {
				return fmt.Errorf("%w: invalid resource number", domain.ErrInvalidInput)
			}
			if kind != "commit" {
				if a.UpdatedAt.IsZero() || a.CreatedAt.IsZero() {
					return fmt.Errorf("%w: GitHub activity is missing timestamps", domain.ErrInvalidInput)
				}
				if a.UpdatedAt.After(cursor) && !a.UpdatedAt.After(start) {
					cursor = a.UpdatedAt
				}
			}
			saved++
			return s.Repositories.SaveActivity(ctx, id, batch, kind, key, n, a, etag)
		}
		parents := url.Values{"state": {"all"}, "sort": {"updated"}, "direction": {"asc"}}
		if since := query.Get("since"); since != "" {
			parents.Set("since", since)
		}
		seen := map[int]bool{}
		err := feed.List(ctx, "issues", parents, func(a github.Activity) error {
			n := a.Number
			if a.Pull == nil {
				return save("issue", n, a, "")
			}
			if seen[n] {
				return nil
			}
			seen[n] = true
			cached, etag, err := s.Repositories.CachedPull(ctx, id, n)
			if err != nil {
				return err
			}
			pr, tag, same, err := feed.Pull(ctx, n, etag)
			if err != nil {
				return err
			}
			if same {
				pr = cached
			}
			if err := save("pull", n, pr, tag); err != nil {
				return err
			}
			if same && !reconcile {
				return nil
			}
			if pr.Commits > 250 {
				return fmt.Errorf("%w: PR exceeds GitHub commit listing limit", domain.ErrInvalidInput)
			}
			links := map[string]bool{}
			err = feed.List(ctx, "pulls/"+strconv.Itoa(n)+"/commits", nil, func(link github.Activity) error {
				if links[link.SHA] {
					return nil
				}
				links[link.SHA] = true
				return save("commit", n, link, "")
			})
			if err != nil {
				return err
			}
			if len(links) != pr.Commits {
				return fmt.Errorf("%w: PR commit count changed during fetch", domain.ErrUpstreamUnavailable)
			}
			return s.Repositories.ClearOldLinks(ctx, id, batch, n)
		})
		if err != nil {
			return err
		}
		for _, source := range []struct{ route, kind string }{{"issues/comments", "comment"}, {"pulls/comments", "review_comment"}} {
			err := feed.List(ctx, source.route, query, func(a github.Activity) error {
				raw := a.IssueURL
				if source.kind == "review_comment" {
					raw = a.PullURL
				}
				u, err := url.Parse(raw)
				if err != nil || !strings.HasSuffix(strings.ToLower(path.Dir(u.Path)), "/repos/"+strings.ToLower(owner+"/"+name)+"/"+strings.Split(source.route, "/")[0]) {
					return fmt.Errorf("%w: invalid comment parent", domain.ErrInvalidInput)
				}
				n, err := strconv.Atoi(path.Base(u.Path))
				if err != nil {
					return fmt.Errorf("%w: invalid comment number", domain.ErrInvalidInput)
				}
				return save(source.kind, n, a, "")
			})
			if err != nil {
				return err
			}
		}
		if reconcile {
			full = start
		}
		if cursor.IsZero() {
			cursor = time.Unix(0, 0).UTC()
		}
		return s.Repositories.FinishActivity(ctx, id, batch, cursor, full, reconcile)
	})
	return saved, err
}
