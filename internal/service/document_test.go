package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"projectlens/internal/domain"
)

type documentProjects map[uuid.UUID]bool

func (p documentProjects) GetByID(_ context.Context, id uuid.UUID) (domain.Project, error) {
	if !p[id] {
		return domain.Project{}, domain.ErrNotFound
	}
	return domain.Project{ID: id}, nil
}

type testDocumentStore struct {
	doc domain.Document
}

func (s *testDocumentStore) Create(_ context.Context, doc domain.Document) (domain.Document, error) {
	doc.Version, doc.ContentBytes = 1, int64(len(doc.Content))
	s.doc = doc
	return doc, nil
}

func (s *testDocumentStore) GetByID(_ context.Context, projectID, docID uuid.UUID) (domain.Document, error) {
	if s.doc.ID != docID || s.doc.ProjectID != projectID {
		return domain.Document{}, domain.ErrNotFound
	}
	return s.doc, nil
}

func (s *testDocumentStore) ListByProject(_ context.Context, _ uuid.UUID, f domain.ListDocumentsFilter) (domain.DocumentListResult, error) {
	return domain.DocumentListResult{Limit: f.Limit, Offset: f.Offset}, nil
}

func (s *testDocumentStore) Update(ctx context.Context, projectID, docID uuid.UUID, version int64, title, content string) (domain.Document, error) {
	return s.Patch(ctx, projectID, docID, version, &title, &content)
}

func (s *testDocumentStore) Patch(ctx context.Context, projectID, docID uuid.UUID, version int64, title, content *string) (domain.Document, error) {
	doc, err := s.GetByID(ctx, projectID, docID)
	if err != nil {
		return doc, err
	}
	if version != doc.Version {
		return domain.Document{}, domain.ErrVersionConflict
	}
	if title != nil {
		doc.Title = *title
	}
	if content != nil {
		doc.Content = *content
	}
	doc.Version++
	s.doc = doc
	return doc, nil
}

func (s *testDocumentStore) Delete(ctx context.Context, projectID, docID uuid.UUID, version *int64) error {
	doc, err := s.GetByID(ctx, projectID, docID)
	if err != nil {
		return err
	}
	if version != nil && *version != doc.Version {
		return domain.ErrVersionConflict
	}
	s.doc = domain.Document{}
	return nil
}

func TestDocumentServiceContentAndTitle(t *testing.T) {
	project := uuid.New()
	s := NewDocumentService(documentProjects{project: true}, &testDocumentStore{}, 4)
	for _, tc := range []struct {
		name, title, content string
		valid                bool
	}{
		{"empty content", " Title ", "", true},
		{"exact bytes", "Title", "abcd", true},
		{"over bytes", "Title", "abcde", false},
		{"exact multibyte", "Title", "éé", true},
		{"over multibyte", "Title", "ééa", false},
		{"blank title", " \t", "", false},
		{"max title", strings.Repeat("界", 200), "", true},
		{"long title", strings.Repeat("界", 201), "", false},
		{"NUL title", "Title\x00", "", false},
		{"NUL content", "Title", "\x00", false},
		{"invalid title UTF-8", "\xff", "", false},
		{"invalid content UTF-8", "Title", "\xff", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := s.Create(t.Context(), project.String(), domain.CreateDocumentInput{Title: tc.title, Content: tc.content})
			if tc.valid {
				if err != nil || doc.ProjectID != project || doc.ID == uuid.Nil || doc.Version != 1 ||
					doc.Title != strings.TrimSpace(tc.title) || doc.ContentBytes != int64(len(tc.content)) {
					t.Fatalf("create = %+v, %v", doc, err)
				}
			} else if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("create = %v, want invalid input", err)
			}
		})
	}
}

func TestDocumentServiceScopingAndVersions(t *testing.T) {
	owner, other := uuid.New(), uuid.New()
	s := NewDocumentService(documentProjects{owner: true, other: true}, &testDocumentStore{}, 0)
	ctx, p := t.Context(), owner.String()
	doc, err := s.Create(ctx, p, domain.CreateDocumentInput{Title: "Original", Content: "Keep this"})
	if err != nil {
		t.Fatal(err)
	}
	id, missing := doc.ID.String(), uuid.NewString()
	title, badContent := " Revised ", "\x00"
	zero, stale := int64(0), int64(9)
	for _, tc := range []struct {
		name string
		run  func() error
		want error
		msg  string
	}{
		{"invalid project UUID", func() error { _, err := s.ListByProject(ctx, "invalid", domain.ListDocumentsFilter{}); return err }, domain.ErrInvalidInput, ""},
		{"invalid document UUID", func() error { _, err := s.GetByID(ctx, p, "invalid"); return err }, domain.ErrInvalidInput, ""},
		{"missing project create", func() error { _, err := s.Create(ctx, missing, domain.CreateDocumentInput{}); return err }, domain.ErrNotFound, "project not found"},
		{"missing project list", func() error { _, err := s.ListByProject(ctx, missing, domain.ListDocumentsFilter{}); return err }, domain.ErrNotFound, "project not found"},
		{"cross-project read", func() error { _, err := s.GetByID(ctx, other.String(), id); return err }, domain.ErrNotFound, "document not found"},
		{"cross-project update", func() error {
			_, err := s.Update(ctx, other.String(), id, domain.UpdateDocumentInput{Title: title, Version: 1})
			return err
		}, domain.ErrNotFound, "document not found"},
		{"cross-project delete", func() error { return s.Delete(ctx, other.String(), id, nil) }, domain.ErrNotFound, "document not found"},
		{"invalid update version", func() error { _, err := s.Update(ctx, p, id, domain.UpdateDocumentInput{Title: title}); return err }, domain.ErrInvalidInput, ""},
		{"invalid patch version", func() error { _, err := s.Patch(ctx, p, id, domain.PatchDocumentInput{Title: &title}); return err }, domain.ErrInvalidInput, ""},
		{"invalid delete version", func() error { return s.Delete(ctx, p, id, &zero) }, domain.ErrInvalidInput, ""},
		{"empty patch", func() error { _, err := s.Patch(ctx, p, id, domain.PatchDocumentInput{Version: 1}); return err }, domain.ErrInvalidInput, ""},
		{"invalid patch content", func() error {
			_, err := s.Patch(ctx, p, id, domain.PatchDocumentInput{Content: &badContent, Version: 1})
			return err
		}, domain.ErrInvalidInput, ""},
		{"stale update", func() error {
			_, err := s.Update(ctx, p, id, domain.UpdateDocumentInput{Title: title, Version: stale})
			return err
		}, domain.ErrVersionConflict, ""},
		{"stale patch", func() error {
			_, err := s.Patch(ctx, p, id, domain.PatchDocumentInput{Title: &title, Version: stale})
			return err
		}, domain.ErrVersionConflict, ""},
		{"stale delete", func() error { return s.Delete(ctx, p, id, &stale) }, domain.ErrVersionConflict, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, tc.want) || (tc.msg != "" && err.Error() != tc.msg) {
				t.Fatalf("error = %v, want %v (%s)", err, tc.want, tc.msg)
			}
		})
	}
	got, err := s.Patch(ctx, p, id, domain.PatchDocumentInput{Title: &title, Version: 1})
	if err != nil || got.Title != "Revised" || got.Content != doc.Content || got.Version != 2 {
		t.Fatalf("patch = %+v, %v", got, err)
	}
}

func TestDocumentServicePagination(t *testing.T) {
	project := uuid.New()
	s := NewDocumentService(documentProjects{project: true}, &testDocumentStore{}, 0)
	for _, tc := range []struct {
		filter domain.ListDocumentsFilter
		limit  int
	}{
		{domain.ListDocumentsFilter{}, 20},
		{domain.ListDocumentsFilter{Limit: 100, Offset: 30}, 100},
		{domain.ListDocumentsFilter{Limit: -1}, 0},
		{domain.ListDocumentsFilter{Limit: 101}, 0},
		{domain.ListDocumentsFilter{Offset: -1}, 0},
	} {
		result, err := s.ListByProject(t.Context(), project.String(), tc.filter)
		if tc.limit == 0 {
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Errorf("filter %+v: %v, want invalid input", tc.filter, err)
			}
		} else if err != nil || result.Limit != tc.limit || result.Offset != tc.filter.Offset {
			t.Errorf("filter %+v: got %+v, %v", tc.filter, result, err)
		}
	}
}
