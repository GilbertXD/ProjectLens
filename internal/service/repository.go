package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"projectlens/internal/domain"
	"projectlens/internal/github"
	"projectlens/internal/gitrepo"
)

type ProjectExistenceChecker interface {
	GetByID(context.Context, uuid.UUID) (domain.Project, error)
}

type RepositoryStore interface {
	Create(context.Context, domain.Repository) (domain.Repository, error)
	GetByID(context.Context, uuid.UUID, uuid.UUID) (domain.Repository, error)
	GetByCanonicalSource(context.Context, uuid.UUID, string) (domain.Repository, error)
	ListByProject(context.Context, uuid.UUID, domain.ListRepositoriesFilter) (domain.RepositoryListResult, error)
	Patch(context.Context, uuid.UUID, uuid.UUID, domain.PatchRepositoryInput) (domain.Repository, error)
	UpdateMetadata(context.Context, domain.Repository) (domain.Repository, error)
	Delete(context.Context, uuid.UUID, uuid.UUID) error
}

type LocalGitInspector interface {
	Inspect(context.Context, string) (gitrepo.Metadata, error)
}

type GitHubMetadataClient interface {
	Fetch(context.Context, string, string) (github.Metadata, error)
}

type RepositoryService struct {
	projects ProjectExistenceChecker
	store    RepositoryStore
	local    LocalGitInspector
	github   GitHubMetadataClient
}

func NewRepositoryService(projects ProjectExistenceChecker, store RepositoryStore, local LocalGitInspector, github GitHubMetadataClient) *RepositoryService {
	return &RepositoryService{projects: projects, store: store, local: local, github: github}
}

func validSourceType(sourceType domain.RepositorySourceType) bool {
	return sourceType == domain.RepositorySourceLocalGit || sourceType == domain.RepositorySourceGitHub
}

func (s *RepositoryService) Associate(ctx context.Context, rawProjectID string, input domain.AssociateRepositoryInput) (domain.Repository, error) {
	projectID, err := s.projectID(ctx, rawProjectID)
	if err != nil {
		return domain.Repository{}, err
	}
	fields, err := normalizeFields(domain.PatchProjectInput{Name: input.Name, Description: input.Description})
	if err != nil {
		return domain.Repository{}, err
	}
	if !validSourceType(input.SourceType) {
		return domain.Repository{}, fmt.Errorf("%w: source_type must be local_git or github", domain.ErrInvalidInput)
	}
	if utf8.RuneCountInString(input.Source) > 2048 {
		return domain.Repository{}, fmt.Errorf("%w: source must contain at most 2048 characters", domain.ErrInvalidInput)
	}
	role := domain.RepositoryRolePrimary
	if input.Role != nil {
		role = *input.Role
		if !role.Valid() {
			return domain.Repository{}, invalidRepositoryRole()
		}
	}
	repo := domain.Repository{Role: role, ID: uuid.New(), ProjectID: projectID, SourceType: input.SourceType, Source: input.Source}
	if err := s.refresh(ctx, &repo); err != nil {
		return domain.Repository{}, err
	}
	if fields.Name != nil {
		repo.Name = *fields.Name
	}
	if fields.Description != nil {
		repo.Description = *fields.Description
	}
	_, err = s.store.GetByCanonicalSource(ctx, projectID, repo.CanonicalSource)
	switch {
	case err == nil:
		return domain.Repository{}, duplicateRepository()
	case !errors.Is(err, domain.ErrNotFound):
		return domain.Repository{}, err
	}
	created, err := s.store.Create(ctx, repo)
	if errors.Is(err, domain.ErrConflict) {
		return domain.Repository{}, duplicateRepository()
	}
	return created, err
}

func invalidRepositoryRole() error {
	return fmt.Errorf("%w: role must be primary or reference", domain.ErrInvalidInput)
}

func duplicateRepository() error {
	return fmt.Errorf("%w: repository is already associated with this project", domain.ErrConflict)
}

func repositoryLookupError(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("%w: repository not found", domain.ErrNotFound)
	}
	return err
}

func (s *RepositoryService) refresh(ctx context.Context, repo *domain.Repository) error {
	switch repo.SourceType {
	case domain.RepositorySourceLocalGit:
		source := repo.Source
		if repo.LocalPath != nil {
			source = *repo.LocalPath
		}
		metadata, err := s.local.Inspect(ctx, source)
		if err != nil {
			return err
		}
		repo.Source = strings.TrimSpace(repo.Source)
		repo.LocalPath = &metadata.CanonicalSource
		repo.CanonicalSource, repo.DefaultBranch, repo.RemoteURL = metadata.CanonicalSource, metadata.DefaultBranch, metadata.RemoteURL
		if repo.Name == "" {
			repo.Name = metadata.Name
		}
	case domain.RepositorySourceGitHub:
		var owner, name string
		if repo.GitHubOwner == nil || repo.GitHubRepo == nil {
			var err error
			owner, name, repo.Source, err = github.ParsePublicRepoURL(repo.Source)
			if err != nil {
				return err
			}
		} else {
			owner, name = *repo.GitHubOwner, *repo.GitHubRepo
		}
		metadata, err := s.github.Fetch(ctx, owner, name)
		if err != nil {
			return err
		}
		repo.CanonicalSource, repo.DefaultBranch, repo.RemoteURL = metadata.CanonicalSource, metadata.DefaultBranch, metadata.RemoteURL
		repo.GitHubOwner, repo.GitHubRepo, repo.GitHubRepoID = &metadata.Owner, &metadata.Repo, &metadata.ID
		repo.GitHubVisibility, repo.GitHubStars, repo.GitHubLanguage = &metadata.Visibility, &metadata.Stars, metadata.Language
		if repo.Name == "" {
			repo.Name, repo.Description = metadata.Repo, metadata.Description
		}
	default:
		return fmt.Errorf("%w: source_type must be local_git or github", domain.ErrInvalidInput)
	}
	now := time.Now().UTC()
	repo.LastSyncedAt = &now
	return nil
}

func (s *RepositoryService) projectID(ctx context.Context, rawID string) (uuid.UUID, error) {
	id, err := parseID(rawID)
	if err != nil {
		return uuid.Nil, err
	}
	_, err = s.projects.GetByID(ctx, id)
	return id, err
}

func (s *RepositoryService) scopedIDs(ctx context.Context, rawProjectID, rawRepoID string) (uuid.UUID, uuid.UUID, error) {
	repoID, err := parseID(rawRepoID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	projectID, err := s.projectID(ctx, rawProjectID)
	return projectID, repoID, err
}

func (s *RepositoryService) ListByProject(ctx context.Context, rawProjectID string, filter domain.ListRepositoriesFilter) (domain.RepositoryListResult, error) {
	projectID, err := s.projectID(ctx, rawProjectID)
	if err != nil {
		return domain.RepositoryListResult{}, err
	}
	if filter.SourceType != "" && !validSourceType(filter.SourceType) {
		return domain.RepositoryListResult{}, fmt.Errorf("%w: source_type must be local_git or github", domain.ErrInvalidInput)
	}
	if filter.Role != "" && !filter.Role.Valid() {
		return domain.RepositoryListResult{}, invalidRepositoryRole()
	}
	if filter.Limit == 0 {
		filter.Limit = 20
	}
	if filter.Limit < 1 || filter.Limit > 100 || filter.Offset < 0 {
		return domain.RepositoryListResult{}, fmt.Errorf("%w: limit must be between 1 and 100 and offset must be nonnegative", domain.ErrInvalidInput)
	}
	return s.store.ListByProject(ctx, projectID, filter)
}

func (s *RepositoryService) GetByID(ctx context.Context, rawProjectID, rawRepoID string) (domain.Repository, error) {
	projectID, repoID, err := s.scopedIDs(ctx, rawProjectID, rawRepoID)
	if err != nil {
		return domain.Repository{}, err
	}
	repo, err := s.store.GetByID(ctx, projectID, repoID)
	return repo, repositoryLookupError(err)
}

func (s *RepositoryService) Patch(ctx context.Context, rawProjectID, rawRepoID string, input domain.PatchRepositoryInput) (domain.Repository, error) {
	projectID, repoID, err := s.scopedIDs(ctx, rawProjectID, rawRepoID)
	if err != nil {
		return domain.Repository{}, err
	}
	if input.Name == nil && input.Description == nil && input.Role == nil {
		return domain.Repository{}, fmt.Errorf("%w: patch must contain at least one field", domain.ErrInvalidInput)
	}
	fields, err := normalizeFields(domain.PatchProjectInput{Name: input.Name, Description: input.Description})
	if err != nil {
		return domain.Repository{}, err
	}
	if input.Role != nil && !input.Role.Valid() {
		return domain.Repository{}, invalidRepositoryRole()
	}
	input.Name, input.Description = fields.Name, fields.Description
	repo, err := s.store.Patch(ctx, projectID, repoID, input)
	return repo, repositoryLookupError(err)
}

func (s *RepositoryService) Sync(ctx context.Context, rawProjectID, rawRepoID string) (domain.Repository, error) {
	repo, err := s.GetByID(ctx, rawProjectID, rawRepoID)
	if err != nil {
		return domain.Repository{}, err
	}
	if err := s.refresh(ctx, &repo); err != nil {
		return domain.Repository{}, err
	}
	updated, err := s.store.UpdateMetadata(ctx, repo)
	if errors.Is(err, domain.ErrConflict) {
		return domain.Repository{}, duplicateRepository()
	}
	return updated, repositoryLookupError(err)
}

func (s *RepositoryService) Delete(ctx context.Context, rawProjectID, rawRepoID string) error {
	projectID, repoID, err := s.scopedIDs(ctx, rawProjectID, rawRepoID)
	if err != nil {
		return err
	}
	return repositoryLookupError(s.store.Delete(ctx, projectID, repoID))
}
