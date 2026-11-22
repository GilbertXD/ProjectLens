package service

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"projectlens/internal/db"
	"projectlens/internal/domain"
)

type ProjectRepository interface {
	Create(context.Context, uuid.UUID, domain.CreateProjectInput) (domain.Project, error)
	GetByID(context.Context, uuid.UUID) (domain.Project, error)
	List(context.Context, domain.ListProjectsFilter) (domain.ProjectListResult, error)
	Update(context.Context, uuid.UUID, domain.UpdateProjectInput) (domain.Project, error)
	Patch(context.Context, uuid.UUID, domain.PatchProjectInput) (domain.Project, error)
	Delete(context.Context, uuid.UUID) (domain.Project, error)
	InsertAuditLog(context.Context, domain.ProjectAuditLog) error
}

type ProjectService struct {
	repo ProjectRepository
	tx   db.TxManager
}

func NewProjectService(repo ProjectRepository, tx db.TxManager) *ProjectService {
	return &ProjectService{repo: repo, tx: tx}
}

var slugPattern = regexp.MustCompile("^[a-z0-9]+(-[a-z0-9]+)*$")

func normalizeFields(input domain.PatchProjectInput) (domain.PatchProjectInput, error) {
	if input.Slug != nil {
		slug := strings.ToLower(strings.TrimSpace(*input.Slug))
		if len(slug) < 2 || len(slug) > 64 || !slugPattern.MatchString(slug) {
			return input, fmt.Errorf("%w: slug must contain 2 to 64 lowercase letters, digits, or separating hyphens", domain.ErrInvalidInput)
		}
		input.Slug = &slug
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" || utf8.RuneCountInString(name) > 120 || strings.ContainsRune(name, 0) {
			return input, fmt.Errorf("%w: name must contain 1 to 120 characters and no NUL", domain.ErrInvalidInput)
		}
		input.Name = &name
	}
	if input.Description != nil && (utf8.RuneCountInString(*input.Description) > 2000 || strings.ContainsRune(*input.Description, 0)) {
		return input, fmt.Errorf("%w: description must contain at most 2000 characters and no NUL", domain.ErrInvalidInput)
	}
	if input.Status != nil && !validStatus(*input.Status) {
		return input, fmt.Errorf("%w: status must be active or archived", domain.ErrInvalidInput)
	}
	return input, nil
}

func validStatus(status domain.ProjectStatus) bool {
	return status == domain.ProjectStatusActive || status == domain.ProjectStatusArchived
}

func parseID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: id must be a UUID", domain.ErrInvalidInput)
	}
	return id, nil
}

func (s *ProjectService) Create(ctx context.Context, input domain.CreateProjectInput) (domain.Project, error) {
	if input.Status == "" {
		input.Status = domain.ProjectStatusActive
	}
	fields, err := normalizeFields(domain.PatchProjectInput{Slug: &input.Slug, Name: &input.Name, Description: &input.Description, Status: &input.Status})
	if err != nil {
		return domain.Project{}, err
	}
	input.Name, input.Slug = *fields.Name, *fields.Slug
	return s.write(ctx, "created", func(ctx context.Context) (domain.Project, error) {
		return s.repo.Create(ctx, uuid.New(), input)
	})
}

func (s *ProjectService) GetByID(ctx context.Context, rawID string) (domain.Project, error) {
	id, err := parseID(rawID)
	if err != nil {
		return domain.Project{}, err
	}
	return s.repo.GetByID(ctx, id)
}

func (s *ProjectService) List(ctx context.Context, filter domain.ListProjectsFilter) (domain.ProjectListResult, error) {
	if filter.Limit == 0 {
		filter.Limit = 20
	}
	if filter.Limit < 1 || filter.Limit > 100 || filter.Offset < 0 {
		return domain.ProjectListResult{}, fmt.Errorf("%w: limit must be between 1 and 100 and offset must be nonnegative", domain.ErrInvalidInput)
	}
	if filter.Status != "" && !validStatus(filter.Status) {
		return domain.ProjectListResult{}, fmt.Errorf("%w: status must be active or archived", domain.ErrInvalidInput)
	}
	return s.repo.List(ctx, filter)
}

func (s *ProjectService) Update(ctx context.Context, rawID string, input domain.UpdateProjectInput) (domain.Project, error) {
	id, err := parseID(rawID)
	if err != nil {
		return domain.Project{}, err
	}
	fields, err := normalizeFields(domain.PatchProjectInput{Slug: &input.Slug, Name: &input.Name, Description: &input.Description, Status: &input.Status})
	if err != nil {
		return domain.Project{}, err
	}
	input.Name, input.Slug = *fields.Name, *fields.Slug
	return s.write(ctx, "updated", func(ctx context.Context) (domain.Project, error) {
		return s.repo.Update(ctx, id, input)
	})
}

func (s *ProjectService) Patch(ctx context.Context, rawID string, input domain.PatchProjectInput) (domain.Project, error) {
	id, err := parseID(rawID)
	if err != nil {
		return domain.Project{}, err
	}
	if input.Name == nil && input.Slug == nil && input.Description == nil && input.Status == nil {
		return domain.Project{}, fmt.Errorf("%w: patch must contain at least one field", domain.ErrInvalidInput)
	}
	input, err = normalizeFields(input)
	if err != nil {
		return domain.Project{}, err
	}
	return s.write(ctx, "updated", func(ctx context.Context) (domain.Project, error) {
		return s.repo.Patch(ctx, id, input)
	})
}

func (s *ProjectService) Delete(ctx context.Context, rawID string) error {
	id, err := parseID(rawID)
	if err != nil {
		return err
	}
	_, err = s.write(ctx, "deleted", func(ctx context.Context) (domain.Project, error) {
		return s.repo.Delete(ctx, id)
	})
	return err
}

func (s *ProjectService) write(ctx context.Context, action string, mutate func(context.Context) (domain.Project, error)) (domain.Project, error) {
	var project domain.Project
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		updated, err := mutate(ctx)
		if err != nil {
			return err
		}
		project = updated
		entry := domain.ProjectAuditLog{ProjectID: &project.ID, ProjectSlug: project.Slug, Action: action}
		if action == "deleted" {
			entry.ProjectID = nil
		}
		return s.repo.InsertAuditLog(ctx, entry)
	})
	if err != nil {
		return domain.Project{}, err
	}
	return project, nil
}
