package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"projectlens/internal/domain"
	"projectlens/internal/github"
	"projectlens/internal/gitrepo"
)

type repositoryProjects map[uuid.UUID]bool

func (p repositoryProjects) GetByID(_ context.Context, id uuid.UUID) (domain.Project, error) {
	if !p[id] {
		return domain.Project{}, domain.ErrNotFound
	}
	return domain.Project{ID: id}, nil
}

type repositoryLocalFunc func(string) (gitrepo.Metadata, error)

func (f repositoryLocalFunc) Inspect(_ context.Context, path string) (gitrepo.Metadata, error) {
	return f(path)
}

type repositoryGitHubFunc func(string, string) (github.Metadata, error)

func (f repositoryGitHubFunc) Fetch(_ context.Context, owner, repo string) (github.Metadata, error) {
	return f(owner, repo)
}

type repositoryServiceStore struct {
	RepositoryStore
	items     map[uuid.UUID]domain.Repository
	createErr error
}

func (s *repositoryServiceStore) GetByCanonicalSource(_ context.Context, project uuid.UUID, source string) (domain.Repository, error) {
	for _, repo := range s.items {
		if repo.ProjectID == project && repo.CanonicalSource == source {
			return repo, nil
		}
	}
	return domain.Repository{}, domain.ErrNotFound
}

func (s *repositoryServiceStore) Create(_ context.Context, repo domain.Repository) (domain.Repository, error) {
	if s.createErr != nil {
		return domain.Repository{}, s.createErr
	}
	s.items[repo.ID] = repo
	return repo, nil
}

func (s *repositoryServiceStore) GetByID(_ context.Context, project, id uuid.UUID) (domain.Repository, error) {
	if repo, ok := s.items[id]; ok && repo.ProjectID == project {
		return repo, nil
	}
	return domain.Repository{}, domain.ErrNotFound
}

func (s *repositoryServiceStore) UpdateMetadata(ctx context.Context, repo domain.Repository) (domain.Repository, error) {
	return s.Create(ctx, repo)
}

func (s *repositoryServiceStore) ListByProject(_ context.Context, _ uuid.UUID, filter domain.ListRepositoriesFilter) (domain.RepositoryListResult, error) {
	return domain.RepositoryListResult{Limit: filter.Limit, Offset: filter.Offset}, nil
}

func (s *repositoryServiceStore) Patch(ctx context.Context, project, id uuid.UUID, input domain.PatchRepositoryInput) (domain.Repository, error) {
	repo, err := s.GetByID(ctx, project, id)
	if err != nil {
		return repo, err
	}
	if input.Name != nil {
		repo.Name = *input.Name
	}
	if input.Description != nil {
		repo.Description = *input.Description
	}
	return s.Create(ctx, repo)
}

func (s *repositoryServiceStore) Delete(ctx context.Context, project, id uuid.UUID) error {
	if _, err := s.GetByID(ctx, project, id); err != nil {
		return err
	}
	delete(s.items, id)
	return nil
}

func TestRepositoryServiceAssociations(t *testing.T) {
	project, other := uuid.New(), uuid.New()
	store := &repositoryServiceStore{items: make(map[uuid.UUID]domain.Repository)}
	local := repositoryLocalFunc(func(path string) (gitrepo.Metadata, error) {
		switch path {
		case "/root/repo", "/root/alias":
			return gitrepo.Metadata{Name: "repo", CanonicalSource: "/root/repo", DefaultBranch: "main"}, nil
		default:
			return gitrepo.Metadata{}, domain.ErrForbidden
		}
	})
	client := repositoryGitHubFunc(func(owner, repo string) (github.Metadata, error) {
		if !strings.EqualFold(owner, "owner") || !strings.EqualFold(repo, "old") {
			t.Fatalf("unexpected fetch: %s/%s", owner, repo)
		}
		return github.Metadata{Owner: "Owner", Repo: "New", ID: 22, CanonicalSource: "github:owner/new", Visibility: "public", Description: "upstream"}, nil
	})
	svc := NewRepositoryService(repositoryProjects{project: true, other: true}, store, local, client)
	ctx := t.Context()
	id := project.String()
	localRepo, err := svc.Associate(ctx, id, domain.AssociateRepositoryInput{SourceType: domain.RepositorySourceLocalGit, Source: "/root/repo"})
	if err != nil || localRepo.LocalPath == nil || *localRepo.LocalPath != "/root/repo" || localRepo.DefaultBranch != "main" || localRepo.ProjectID != project || localRepo.LastSyncedAt == nil {
		t.Fatalf("local association: %+v %v", localRepo, err)
	}
	name := " custom "
	ghRepo, err := svc.Associate(ctx, id, domain.AssociateRepositoryInput{SourceType: domain.RepositorySourceGitHub, Source: "  https://WWW.GITHUB.COM/owner/old.git/  ", Name: &name})
	if err != nil || ghRepo.Name != "custom" || ghRepo.Description != "upstream" || ghRepo.GitHubRepoID == nil ||
		*ghRepo.GitHubRepoID != 22 || ghRepo.CanonicalSource != "github:owner/new" || ghRepo.Source != "https://github.com/owner/old" {
		t.Fatalf("GitHub association: %+v %v", ghRepo, err)
	}
	blank, nul, long := " ", "bad\x00", strings.Repeat("界", 2001)
	for _, tc := range []struct {
		id    string
		input domain.AssociateRepositoryInput
		want  error
	}{
		{id, domain.AssociateRepositoryInput{SourceType: domain.RepositorySourceLocalGit, Source: "/root/alias"}, domain.ErrConflict},
		{id, domain.AssociateRepositoryInput{SourceType: domain.RepositorySourceGitHub, Source: "https://WWW.GITHUB.COM/OWNER/OLD.git/"}, domain.ErrConflict},
		{other.String(), domain.AssociateRepositoryInput{SourceType: domain.RepositorySourceLocalGit, Source: "/root/alias"}, nil},
		{"bad", domain.AssociateRepositoryInput{}, domain.ErrInvalidInput},
		{uuid.NewString(), domain.AssociateRepositoryInput{}, domain.ErrNotFound},
		{id, domain.AssociateRepositoryInput{SourceType: "other"}, domain.ErrInvalidInput},
		{id, domain.AssociateRepositoryInput{SourceType: domain.RepositorySourceLocalGit, Source: "/outside"}, domain.ErrForbidden},
		{id, domain.AssociateRepositoryInput{SourceType: domain.RepositorySourceGitHub, Source: "http://github.com/owner/old"}, domain.ErrInvalidInput},
		{id, domain.AssociateRepositoryInput{Name: &blank}, domain.ErrInvalidInput},
		{id, domain.AssociateRepositoryInput{Name: &nul}, domain.ErrInvalidInput},
		{id, domain.AssociateRepositoryInput{Description: &long}, domain.ErrInvalidInput},
	} {
		if _, err := svc.Associate(ctx, tc.id, tc.input); !errors.Is(err, tc.want) {
			t.Errorf("associate %+v: got %v, want %v", tc.input, err, tc.want)
		}
	}
	store.createErr = domain.ErrConflict
	if _, err := svc.Associate(ctx, other.String(), domain.AssociateRepositoryInput{SourceType: domain.RepositorySourceGitHub, Source: "https://github.com/owner/old"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("concurrent duplicate: %v", err)
	}
}

func TestRepositoryServiceManagement(t *testing.T) {
	project, other, id := uuid.New(), uuid.New(), uuid.New()
	repo := domain.Repository{ID: id, ProjectID: project, SourceType: domain.RepositorySourceLocalGit, Source: "/root/alias", Name: "old"}
	canonical := "/root/repo"
	repo.LocalPath = &canonical
	store := &repositoryServiceStore{items: map[uuid.UUID]domain.Repository{id: repo}}
	svc := NewRepositoryService(repositoryProjects{project: true, other: true}, store, repositoryLocalFunc(func(path string) (gitrepo.Metadata, error) {
		if path != canonical {
			t.Fatalf("sync inspected %q", path)
		}
		return gitrepo.Metadata{CanonicalSource: canonical, DefaultBranch: "updated"}, nil
	}), nil)
	ctx := t.Context()
	if _, err := svc.GetByID(ctx, other.String(), id.String()); !errors.Is(err, domain.ErrNotFound) || !strings.Contains(err.Error(), ": repository not found") {
		t.Fatalf("cross-project get: %v", err)
	}
	if _, err := svc.GetByID(ctx, project.String(), "bad"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("invalid repository ID: %v", err)
	}
	if _, err := svc.Patch(ctx, project.String(), id.String(), domain.PatchRepositoryInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("empty patch: %v", err)
	}
	name, description := " renamed ", "custom"
	if got, err := svc.Patch(ctx, project.String(), id.String(), domain.PatchRepositoryInput{Name: &name, Description: &description}); err != nil || got.Name != "renamed" {
		t.Fatalf("patch: %+v %v", got, err)
	}
	got, err := svc.Sync(ctx, project.String(), id.String())
	if err != nil || got.LastSyncedAt == nil || got.Name != "renamed" || got.Description != description || got.DefaultBranch != "updated" {
		t.Fatalf("sync: %+v %v", got, err)
	}
	store.createErr = domain.ErrConflict
	if _, err := svc.Sync(ctx, project.String(), id.String()); !errors.Is(err, domain.ErrConflict) || !strings.HasSuffix(err.Error(), "repository is already associated with this project") {
		t.Fatalf("sync conflict: %v", err)
	}
	store.createErr = nil
	if err := svc.Delete(ctx, other.String(), id.String()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-project delete: %v", err)
	}
	if err := svc.Delete(ctx, project.String(), id.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Sync(ctx, project.String(), id.String()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sync of deleted repository: %v", err)
	}
	if list, err := svc.ListByProject(ctx, project.String(), domain.ListRepositoriesFilter{}); err != nil || list.Limit != 20 {
		t.Fatalf("default list: %+v %v", list, err)
	}
	for _, filter := range []domain.ListRepositoriesFilter{{Limit: -1}, {Limit: 101}, {Offset: -1}, {SourceType: "other"}} {
		if _, err := svc.ListByProject(ctx, project.String(), filter); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("filter %+v: %v", filter, err)
		}
	}
}
