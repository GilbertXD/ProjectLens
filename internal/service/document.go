package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

type DocumentStore interface {
	Create(context.Context, domain.Document) (domain.Document, error)
	GetScopedByID(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) (domain.Document, error)
	ListByProject(context.Context, uuid.UUID, domain.ListDocumentsFilter) (domain.DocumentListResult, error)
	Update(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, int64, string, string, domain.OriginPatch) (domain.Document, error)
	Patch(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, int64, *string, *string, domain.OriginPatch) (domain.Document, error)
	Delete(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, *int64) error
}

type DocumentService struct {
	projects        ProjectExistenceChecker
	store           DocumentStore
	maxContentBytes int64
}

func NewDocumentService(projects ProjectExistenceChecker, store DocumentStore, maxContentBytes int64) *DocumentService {
	if maxContentBytes <= 0 {
		maxContentBytes = config.DefaultDocumentMaxContentBytes
	}
	return &DocumentService{projects: projects, store: store, maxContentBytes: maxContentBytes}
}

func (s *DocumentService) projectID(ctx context.Context, rawID string) (uuid.UUID, error) {
	id, err := parseID(rawID)
	if err != nil {
		return uuid.Nil, err
	}
	_, err = s.projects.GetByID(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		err = domain.ErrNotFound
	}
	return id, err
}

func (s *DocumentService) documentIDs(ctx context.Context, rawProjectID, rawDocID string) (uuid.UUID, uuid.UUID, error) {
	projectID, err := s.projectID(ctx, rawProjectID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	docID, err := parseID(rawDocID)
	return projectID, docID, err
}

type documentNotFound struct{}

func (documentNotFound) Error() string { return "document not found" }
func (documentNotFound) Unwrap() error { return domain.ErrNotFound }

func documentError(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return documentNotFound{}
	}
	return err
}

func (s *DocumentService) validateDocumentFields(title, content *string) (*string, error) {
	if title != nil {
		trimmed := strings.TrimSpace(*title)
		if trimmed == "" || utf8.RuneCountInString(trimmed) > 200 || !utf8.ValidString(trimmed) || strings.ContainsRune(trimmed, 0) {
			return nil, fmt.Errorf("%w: title must contain 1 to 200 characters of valid UTF-8 without NUL", domain.ErrInvalidInput)
		}
		title = &trimmed
	}
	if content != nil {
		if !utf8.ValidString(*content) || strings.ContainsRune(*content, 0) {
			return nil, fmt.Errorf("%w: content must be valid UTF-8 without NUL", domain.ErrInvalidInput)
		}
		if int64(len(*content)) > s.maxContentBytes {
			return nil, fmt.Errorf("%w: content must be at most %d UTF-8 bytes", domain.ErrInvalidInput, s.maxContentBytes)
		}
	}
	return title, nil
}

func validateDocumentVersion(version int64) error {
	if version < 1 {
		return fmt.Errorf("%w: version must be at least 1", domain.ErrInvalidInput)
	}
	return nil
}

// validateDocumentOrigin trims and checks informational provenance. Provenance
// never grants scope, so it may name a repository that is no longer associated.
func validateDocumentOrigin(origin *domain.DocumentOrigin) (*domain.DocumentOrigin, error) {
	if origin == nil {
		return nil, nil
	}
	name := strings.TrimSpace(origin.RepositoryName)
	if origin.RepositoryID == uuid.Nil || name == "" || utf8.RuneCountInString(name) > 200 ||
		!utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return nil, fmt.Errorf("%w: origin must contain a repository_id and 1 to 200 characters of valid UTF-8 repository_name", domain.ErrInvalidInput)
	}
	return &domain.DocumentOrigin{RepositoryID: origin.RepositoryID, RepositoryName: name}, nil
}

func (s *DocumentService) Create(ctx context.Context, rawProjectID string, input domain.CreateDocumentInput) (domain.Document, error) {
	projectID, err := s.projectID(ctx, rawProjectID)
	if err != nil {
		return domain.Document{}, err
	}
	title, err := s.validateDocumentFields(&input.Title, &input.Content)
	if err != nil {
		return domain.Document{}, err
	}
	return s.store.Create(ctx, domain.Document{
		ID: uuid.New(), ProjectID: projectID, Title: *title, Content: input.Content,
		RepositoryID: input.RepositoryID, Scope: domain.DocumentScope(input.RepositoryID),
	})
}

func (s *DocumentService) ListByProject(ctx context.Context, rawProjectID string, filter domain.ListDocumentsFilter) (domain.DocumentListResult, error) {
	projectID, err := s.projectID(ctx, rawProjectID)
	if err != nil {
		return domain.DocumentListResult{}, err
	}
	if filter.Limit == 0 {
		filter.Limit = 20
	}
	if filter.Limit < 1 || filter.Limit > 100 || filter.Offset < 0 {
		return domain.DocumentListResult{}, fmt.Errorf("%w: limit must be between 1 and 100 and offset must be nonnegative", domain.ErrInvalidInput)
	}
	switch filter.Scope {
	case domain.KnowledgeScopeProject:
		if filter.RepositoryID != nil {
			return domain.DocumentListResult{}, fmt.Errorf("%w: project scope cannot be combined with repository_id", domain.ErrInvalidInput)
		}
	case "", domain.KnowledgeScopeRepository:
	default:
		return domain.DocumentListResult{}, fmt.Errorf("%w: scope must be project or repository", domain.ErrInvalidInput)
	}
	return s.store.ListByProject(ctx, projectID, filter)
}

func (s *DocumentService) GetByID(ctx context.Context, rawProjectID, rawDocID string, repositoryID *uuid.UUID) (domain.Document, error) {
	projectID, docID, err := s.documentIDs(ctx, rawProjectID, rawDocID)
	if err != nil {
		return domain.Document{}, err
	}
	doc, err := s.store.GetScopedByID(ctx, projectID, docID, repositoryID)
	return doc, documentError(err)
}

func (s *DocumentService) Update(ctx context.Context, rawProjectID, rawDocID string, repositoryID *uuid.UUID, input domain.UpdateDocumentInput) (domain.Document, error) {
	projectID, docID, err := s.documentIDs(ctx, rawProjectID, rawDocID)
	if err != nil {
		return domain.Document{}, err
	}
	if err := validateDocumentVersion(input.Version); err != nil {
		return domain.Document{}, err
	}
	title, err := s.validateDocumentFields(&input.Title, &input.Content)
	if err != nil {
		return domain.Document{}, err
	}
	origin, err := validateDocumentOrigin(input.Origin)
	if err != nil {
		return domain.Document{}, err
	}
	doc, err := s.store.Update(ctx, projectID, docID, repositoryID, input.Version, *title, input.Content,
		domain.OriginPatch{Origin: origin, Clear: input.Origin == nil})
	return doc, documentError(err)
}

func (s *DocumentService) Patch(ctx context.Context, rawProjectID, rawDocID string, repositoryID *uuid.UUID, input domain.PatchDocumentInput) (domain.Document, error) {
	projectID, docID, err := s.documentIDs(ctx, rawProjectID, rawDocID)
	if err != nil {
		return domain.Document{}, err
	}
	if err := validateDocumentVersion(input.Version); err != nil {
		return domain.Document{}, err
	}
	if input.Title == nil && input.Content == nil && input.Origin == nil && !input.ClearOrigin {
		return domain.Document{}, fmt.Errorf("%w: patch must contain title, content, or origin", domain.ErrInvalidInput)
	}
	title, err := s.validateDocumentFields(input.Title, input.Content)
	if err != nil {
		return domain.Document{}, err
	}
	origin, err := validateDocumentOrigin(input.Origin)
	if err != nil {
		return domain.Document{}, err
	}
	doc, err := s.store.Patch(ctx, projectID, docID, repositoryID, input.Version, title, input.Content,
		domain.OriginPatch{Origin: origin, Clear: input.ClearOrigin})
	return doc, documentError(err)
}

func (s *DocumentService) Delete(ctx context.Context, rawProjectID, rawDocID string, repositoryID *uuid.UUID, expectedVersion *int64) error {
	projectID, docID, err := s.documentIDs(ctx, rawProjectID, rawDocID)
	if err != nil {
		return err
	}
	if expectedVersion != nil {
		if err := validateDocumentVersion(*expectedVersion); err != nil {
			return err
		}
	}
	return documentError(s.store.Delete(ctx, projectID, docID, repositoryID, expectedVersion))
}
