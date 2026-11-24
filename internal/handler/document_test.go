package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/domain"
)

type documentServiceStub struct {
	repositoryID *uuid.UUID
	version      int64
	filter       domain.ListDocumentsFilter
	err          error
}

func (s *documentServiceStub) Create(_ context.Context, _ string, input domain.CreateDocumentInput) (domain.Document, error) {
	s.repositoryID = input.RepositoryID
	return domain.Document{Title: input.Title, Content: input.Content, Version: 1}, s.err
}

func (s *documentServiceStub) GetByID(_ context.Context, _, _ string, repositoryID *uuid.UUID) (domain.Document, error) {
	s.repositoryID = repositoryID
	return domain.Document{Version: 1}, s.err
}

func (s *documentServiceStub) ListByProject(_ context.Context, _ string, filter domain.ListDocumentsFilter) (domain.DocumentListResult, error) {
	s.repositoryID = filter.RepositoryID
	s.filter = filter
	return domain.DocumentListResult{Items: []domain.DocumentSummary{}, Limit: filter.Limit, Offset: filter.Offset}, s.err
}

func (s *documentServiceStub) Update(_ context.Context, _, _ string, repositoryID *uuid.UUID, input domain.UpdateDocumentInput) (domain.Document, error) {
	s.repositoryID = repositoryID
	s.version = input.Version
	return domain.Document{Title: input.Title, Content: input.Content, Version: input.Version + 1}, s.err
}

func (s *documentServiceStub) Patch(_ context.Context, _, _ string, repositoryID *uuid.UUID, input domain.PatchDocumentInput) (domain.Document, error) {
	s.repositoryID = repositoryID
	s.version = input.Version
	if input.Title == nil && input.Content == nil {
		return domain.Document{}, domain.ErrInvalidInput
	}
	return domain.Document{Version: input.Version + 1}, s.err
}

func (s *documentServiceStub) Delete(_ context.Context, _, _ string, repositoryID *uuid.UUID, version *int64) error {
	s.repositoryID = repositoryID
	if version != nil {
		s.version = *version
	}
	return s.err
}

func TestDocumentHandler(t *testing.T) {
	const base = "/api/v1/projects/project/documents"
	const item = base + "/doc"
	for _, tc := range []struct {
		name, method, path, body, ifMatch string
		serviceErr                        error
		status                            int
		code, etag                        string
		version                           int64
	}{
		{name: "create", method: "POST", path: base, body: `{"title":"Title"}`, status: 201, etag: `"1"`},
		{name: "read", method: "GET", path: item, status: 200, etag: `"1"`},
		{name: "PUT body version", method: "PUT", path: item, body: `{"title":"Title","version":2}`, status: 200, etag: `"3"`, version: 2},
		{name: "PUT strong ETag", method: "PUT", path: item, body: `{"title":"Title"}`, ifMatch: `"2"`, status: 200, etag: `"3"`, version: 2},
		{name: "PATCH weak ETag", method: "PATCH", path: item, body: `{"content":""}`, ifMatch: `W/"2"`, status: 200, etag: `"3"`, version: 2},
		{name: "PATCH matching version", method: "PATCH", path: item, body: `{"title":"Title","version":2}`, ifMatch: "2", status: 200, etag: `"3"`, version: 2},
		{name: "delete", method: "DELETE", path: item, status: 204},
		{name: "conditional delete", method: "DELETE", path: item, ifMatch: "2", status: 204, version: 2},
		{name: "PUT missing version", method: "PUT", path: item, body: `{"title":"Title"}`, status: 400, code: "invalid_input"},
		{name: "PATCH missing version", method: "PATCH", path: item, body: `{"content":"x"}`, status: 400, code: "invalid_input"},
		{name: "PUT mismatch", method: "PUT", path: item, body: `{"title":"Title","version":1}`, ifMatch: "2", status: 400, code: "invalid_input"},
		{name: "PATCH mismatch", method: "PATCH", path: item, body: `{"content":"x","version":1}`, ifMatch: "2", status: 400, code: "invalid_input"},
		{name: "PATCH empty", method: "PATCH", path: item, body: `{"version":1}`, status: 400, code: "invalid_input", version: 1},
		{name: "invalid version", method: "PUT", path: item, body: `{"title":"Title","version":null}`, ifMatch: "1", status: 400, code: "invalid_input"},
		{name: "malformed ETag", method: "PUT", path: item, body: `{"title":"Title"}`, ifMatch: `W/"2`, status: 400, code: "invalid_input"},
		{name: "multiple ETags", method: "DELETE", path: item, ifMatch: `"1", "2"`, status: 400, code: "invalid_input"},
		{name: "zero ETag", method: "DELETE", path: item, ifMatch: `"0"`, status: 400, code: "invalid_input"},
		{name: "unknown field", method: "POST", path: base, body: `{"title":"Title","typo":true}`, status: 400, code: "invalid_input"},
		{name: "malformed JSON", method: "POST", path: base, body: `{"title":`, status: 400, code: "invalid_input"},
		{name: "invalid UTF-8", method: "POST", path: base, body: "{\"title\":\"\xff\"}", status: 400, code: "invalid_input"},
		{name: "null body", method: "POST", path: base, body: "null", status: 400, code: "invalid_input"},
		{name: "extra JSON value", method: "POST", path: base, body: `{"title":"Title"} {}`, status: 400, code: "invalid_input"},
		{name: "missing document", method: "GET", path: item, serviceErr: domain.ErrNotFound, status: 404, code: "not_found"},
		{name: "stale version", method: "DELETE", path: item, ifMatch: "1", serviceErr: domain.ErrVersionConflict, status: 409, code: "version_conflict", version: 1},
		{name: "raw body ceiling", method: "POST", path: base, body: `{"title":"Title"}` + strings.Repeat(" ", 256<<10), status: 413, code: "payload_too_large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &documentServiceStub{err: tc.serviceErr}
			r := documentTestRouter(svc)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.ifMatch != "" {
				req.Header.Set("If-Match", tc.ifMatch)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status || w.Header().Get("ETag") != tc.etag || svc.version != tc.version {
				t.Fatalf("status = %d, ETag = %q, input version = %d, body = %s", w.Code, w.Header().Get("ETag"), svc.version, w.Body)
			}
			if tc.code != "" {
				var response map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response["code"] != tc.code {
					t.Fatalf("error response = %s, want code %s", w.Body, tc.code)
				}
			}
		})
	}
}

func documentTestRouter(svc DocumentService) *gin.Engine {
	r := gin.New()
	NewDocumentHandler(svc, slog.New(slog.NewTextHandler(io.Discard, nil)), 8).Register(r.Group("/api/v1"))
	return r
}

func TestDocumentHandlerPagination(t *testing.T) {
	svc := &documentServiceStub{}
	r := documentTestRouter(svc)
	const base = "/api/v1/projects/project/documents"
	for _, query := range []string{"limit=", "limit=0", "limit=-1", "limit=101", "limit=1.5", "limit=1&limit=2", "offset=-1", "offset=abc"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", base+"?"+query, nil))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_input") {
			t.Errorf("%s = %d %s", query, w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", base+"?limit=5&offset=3", nil))
	if w.Code != 200 || svc.filter != (domain.ListDocumentsFilter{Limit: 5, Offset: 3}) {
		t.Fatalf("list = %d, filter = %+v", w.Code, svc.filter)
	}
}

func TestDocumentHandlerRepositoryRoutes(t *testing.T) {
	repo, otherRepo := uuid.New(), uuid.New()
	project := "/api/v1/projects/project"
	base := project + "/repositories/" + repo.String() + "/documents"
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", base, `{"title":"Note"}`, 201},
		{"POST", base, `{"title":"Note","repository_id":"` + repo.String() + `"}`, 201},
		{"POST", base, `{"title":"Note","repository_id":"` + otherRepo.String() + `"}`, 400},
		{"GET", base, "", 200},
		{"GET", base + "/doc", "", 200},
		{"PUT", base + "/doc", `{"title":"Note","version":1}`, 200},
		{"PATCH", base + "/doc", `{"title":"Note","version":1}`, 200},
		{"DELETE", base + "/doc", "", 204},
		{"GET", project + "/documents?scope=repository&repository_id=" + repo.String(), "", 200},
		{"GET", project + "/documents?scope=project&repository_id=" + repo.String(), "", 400},
		{"GET", base + "?repository_id=" + otherRepo.String(), "", 400},
		{"GET", base + "?scope=project", "", 400},
		{"GET", project + "/documents?scope=", "", 400},
		{"GET", project + "/documents?repository_id=bad", "", 400},
		{"GET", project + "/repositories/invalid/documents", "", 400},
	} {
		svc := &documentServiceStub{}
		w := httptest.NewRecorder()
		documentTestRouter(svc).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Errorf("%s %s = %d %s, want %d", tc.method, tc.path, w.Code, w.Body, tc.status)
		}
		if tc.status < 400 && (svc.repositoryID == nil || *svc.repositoryID != repo) {
			t.Errorf("%s %s forwarded repository %v, want %s", tc.method, tc.path, svc.repositoryID, repo)
		}
	}
}
