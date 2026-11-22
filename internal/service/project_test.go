package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"projectlens/internal/domain"
)

type memoryProjects struct {
	project  domain.Project
	audits   []domain.ProjectAuditLog
	auditErr error
	filter   domain.ListProjectsFilter
}

func (r *memoryProjects) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	before := *r
	if err := fn(ctx); err != nil {
		*r = before
		return err
	}
	return nil
}

func (r *memoryProjects) Create(_ context.Context, id uuid.UUID, input domain.CreateProjectInput) (domain.Project, error) {
	if r.project.Slug == input.Slug {
		return domain.Project{}, domain.ErrConflict
	}
	r.project = domain.Project{ID: id, Slug: input.Slug, Name: input.Name, Description: input.Description, Status: input.Status}
	return r.project, nil
}

func (r *memoryProjects) GetByID(_ context.Context, id uuid.UUID) (domain.Project, error) {
	if r.project.ID == uuid.Nil || id != r.project.ID {
		return domain.Project{}, domain.ErrNotFound
	}
	return r.project, nil
}

func (r *memoryProjects) List(_ context.Context, filter domain.ListProjectsFilter) (domain.ProjectListResult, error) {
	r.filter = filter
	return domain.ProjectListResult{}, nil
}

func (r *memoryProjects) Update(ctx context.Context, id uuid.UUID, input domain.UpdateProjectInput) (domain.Project, error) {
	return r.Patch(ctx, id, domain.PatchProjectInput{Name: &input.Name, Slug: &input.Slug, Description: &input.Description, Status: &input.Status})
}

func (r *memoryProjects) Patch(ctx context.Context, id uuid.UUID, input domain.PatchProjectInput) (domain.Project, error) {
	p, err := r.GetByID(ctx, id)
	if err != nil {
		return p, err
	}
	if input.Name != nil {
		p.Name = *input.Name
	}
	if input.Slug != nil {
		p.Slug = *input.Slug
	}
	if input.Description != nil {
		p.Description = *input.Description
	}
	if input.Status != nil {
		p.Status = *input.Status
	}
	r.project = p
	return p, nil
}

func (r *memoryProjects) Delete(ctx context.Context, id uuid.UUID) (domain.Project, error) {
	p, err := r.GetByID(ctx, id)
	if err == nil {
		r.project = domain.Project{}
	}
	return p, err
}

func (r *memoryProjects) InsertAuditLog(_ context.Context, entry domain.ProjectAuditLog) error {
	if r.auditErr != nil {
		return r.auditErr
	}
	r.audits = append(r.audits, entry)
	return nil
}

func TestProjectServiceCRUD(t *testing.T) {
	repo := &memoryProjects{}
	svc := NewProjectService(repo, repo)
	ctx := t.Context()
	input := domain.CreateProjectInput{Name: "  Example  ", Slug: " EX-AMPLE ", Description: " spaced "}
	created, err := svc.Create(ctx, input)
	if err != nil || created.ID == uuid.Nil || created.Name != "Example" || created.Slug != "ex-ample" ||
		created.Status != domain.ProjectStatusActive || created.Description != input.Description {
		t.Fatalf("create = %+v, error = %v", created, err)
	}
	if _, err := svc.Create(ctx, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("conflict = %v", err)
	}
	id := created.ID.String()
	if got, err := svc.GetByID(ctx, id); err != nil || got != created {
		t.Fatalf("get = %+v, error = %v", got, err)
	}
	if _, err := svc.List(ctx, domain.ListProjectsFilter{}); err != nil || repo.filter.Limit != 20 {
		t.Fatalf("list filter = %+v, error = %v", repo.filter, err)
	}
	name := "  Patched  "
	if got, err := svc.Patch(ctx, id, domain.PatchProjectInput{Name: &name}); err != nil || got.Name != "Patched" || got.Slug != created.Slug {
		t.Fatalf("patch = %+v, error = %v", got, err)
	}
	update := domain.UpdateProjectInput{Name: " Updated ", Slug: " NEW-SLUG ", Status: domain.ProjectStatusArchived}
	if got, err := svc.Update(ctx, id, update); err != nil || got.Name != "Updated" || got.Slug != "new-slug" || got.Status != update.Status || got.Description != "" {
		t.Fatalf("update = %+v, error = %v", got, err)
	}
	if err := svc.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetByID(ctx, id); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	actions := []string{"created", "updated", "updated", "deleted"}
	if len(repo.audits) != len(actions) {
		t.Fatalf("audit log = %+v", repo.audits)
	}
	for i, entry := range repo.audits {
		if entry.Action != actions[i] {
			t.Errorf("audit entry = %+v, want %s", entry, actions[i])
		}
	}
	if entry := repo.audits[3]; entry.ProjectSlug != "new-slug" || entry.ProjectID != nil {
		t.Errorf("unexpected delete audit entry: %+v", entry)
	}
}

func TestProjectServiceValidation(t *testing.T) {
	repo := &memoryProjects{}
	svc := NewProjectService(repo, repo)
	ctx := t.Context()
	for _, input := range []domain.CreateProjectInput{
		{Slug: "a", Name: "Example"},
		{Slug: "a--b", Name: "Example"},
		{Slug: strings.Repeat("a", 65), Name: "Example"},
		{Slug: "valid", Name: " \t "},
		{Slug: "valid", Name: strings.Repeat("名", 121)},
		{Slug: "valid", Name: "Example", Description: strings.Repeat("名", 2001)},
		{Slug: "valid", Name: "Example", Status: "unknown"},
	} {
		if _, err := svc.Create(ctx, input); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("expected invalid create, got %v", err)
		}
	}
	for _, filter := range []domain.ListProjectsFilter{{Limit: -1}, {Limit: 101}, {Offset: -1}, {Status: "unknown"}} {
		if _, err := svc.List(ctx, filter); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("expected invalid list for %+v, got %v", filter, err)
		}
	}
	if _, err := svc.GetByID(ctx, "not-a-uuid"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("invalid ID: %v", err)
	}
	if _, err := svc.Patch(ctx, uuid.NewString(), domain.PatchProjectInput{}); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("empty patch: %v", err)
	}
	if len(repo.audits) != 0 || repo.project.ID != uuid.Nil {
		t.Fatal("invalid requests changed repository")
	}
}

func TestProjectServiceAuditFailure(t *testing.T) {
	failure := errors.New("audit insert failed")
	repo := &memoryProjects{auditErr: failure}
	svc := NewProjectService(repo, repo)
	if _, err := svc.Create(t.Context(), domain.CreateProjectInput{Slug: "rollback", Name: "Rollback"}); !errors.Is(err, failure) {
		t.Fatalf("write error = %v, want audit failure", err)
	}
	if repo.project.ID != uuid.Nil || len(repo.audits) != 0 {
		t.Fatalf("rollback did not restore repository: %+v", repo)
	}
}
