package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/domain"
)

type projectStub struct {
	err error
}

func (s projectStub) Create(_ context.Context, input domain.CreateProjectInput) (domain.Project, error) {
	return domain.Project{Name: input.Name, Slug: input.Slug}, s.err
}

func (s projectStub) List(_ context.Context, filter domain.ListProjectsFilter) (domain.ProjectListResult, error) {
	return domain.ProjectListResult{Items: []domain.Project{}, Limit: filter.Limit, Offset: filter.Offset}, s.err
}

func (s projectStub) GetByID(_ context.Context, id string) (domain.Project, error) {
	projectID, err := uuid.Parse(id)
	if err != nil {
		return domain.Project{}, domain.ErrInvalidInput
	}
	return domain.Project{ID: projectID, Name: "Sample"}, s.err
}

func (s projectStub) Update(_ context.Context, id string, input domain.UpdateProjectInput) (domain.Project, error) {
	return domain.Project{ID: uuid.MustParse(id), Name: input.Name, Slug: input.Slug}, s.err
}

func (s projectStub) Patch(_ context.Context, id string, input domain.PatchProjectInput) (domain.Project, error) {
	project := domain.Project{ID: uuid.MustParse(id)}
	if input.Name != nil {
		project.Name = *input.Name
	}
	return project, s.err
}

func (s projectStub) Delete(_ context.Context, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return domain.ErrInvalidInput
	}
	return s.err
}

func TestProjectHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const projectID = "8d3590cf-6d9e-436f-a732-64b82fb6603f"
	for _, tc := range []struct {
		name, method, query, body string
		id, projectName           string
		err                       error
		want                      int
		code                      string
	}{
		{name: "create", method: "POST", body: `{"slug":"sample","name":"Sample"}`, want: 201},
		{name: "unknown field", method: "POST", body: `{"extra":1}`, want: 400, code: "invalid_input"},
		{name: "second value", method: "POST", body: `{} {}`, want: 400, code: "invalid_input"},
		{name: "null body", method: "POST", body: `null`, want: 400, code: "invalid_input"},
		{name: "invalid JSON", method: "POST", body: `{`, want: 400, code: "invalid_input"},
		{name: "invalid input", method: "POST", body: `{}`, err: fmt.Errorf("%w: bad name", domain.ErrInvalidInput), want: 400, code: "invalid_input"},
		{name: "missing", method: "POST", body: `{}`, err: domain.ErrNotFound, want: 404, code: "not_found"},
		{name: "conflict", method: "POST", body: `{}`, err: domain.ErrConflict, want: 409, code: "conflict"},
		{name: "internal", method: "POST", body: `{}`, err: errors.New("sensitive SQL details"), want: 500, code: "internal_error"},
		{name: "list", method: "GET", query: "?limit=2&offset=1", want: 200},
		{name: "bad limit", method: "GET", query: "?limit=abc", want: 400, code: "invalid_input"},
		{name: "zero limit", method: "GET", query: "?limit=0", want: 400, code: "invalid_input"},
		{name: "empty status", method: "GET", query: "?status=", want: 400, code: "invalid_input"},
		{name: "get", method: "GET", id: projectID, want: 200, projectName: "Sample"},
		{name: "get missing", method: "GET", id: projectID, err: domain.ErrNotFound, want: 404, code: "not_found"},
		{name: "get invalid ID", method: "GET", id: "invalid", want: 400, code: "invalid_input"},
		{name: "update", method: "PUT", id: projectID, body: `{"name":"Updated","slug":"updated","status":"active"}`, want: 200, projectName: "Updated"},
		{name: "update invalid JSON", method: "PUT", id: projectID, body: `{`, want: 400, code: "invalid_input"},
		{name: "update unknown field", method: "PUT", id: projectID, body: `{"name":"Updated","extra":1}`, want: 400, code: "invalid_input"},
		{name: "update conflict", method: "PUT", id: projectID, body: `{"name":"Updated","slug":"taken","status":"active"}`, err: domain.ErrConflict, want: 409, code: "conflict"},
		{name: "patch", method: "PATCH", id: projectID, body: `{"name":"Patched"}`, want: 200, projectName: "Patched"},
		{name: "patch invalid JSON", method: "PATCH", id: projectID, body: `{`, want: 400, code: "invalid_input"},
		{name: "patch missing", method: "PATCH", id: projectID, body: `{"name":"Patched"}`, err: domain.ErrNotFound, want: 404, code: "not_found"},
		{name: "delete", method: "DELETE", id: projectID, want: 204},
		{name: "delete missing", method: "DELETE", id: projectID, err: domain.ErrNotFound, want: 404, code: "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			NewProjectHandler(projectStub{err: tc.err}, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(engine.Group("/api/v1"))
			w := httptest.NewRecorder()
			path := "/api/v1/projects"
			if tc.id != "" {
				path += "/" + tc.id
			}
			engine.ServeHTTP(w, httptest.NewRequest(tc.method, path+tc.query, strings.NewReader(tc.body)))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body)
			}
			if tc.code != "" {
				var body map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["code"] != tc.code {
					t.Fatalf("body = %s, want error code %q", w.Body, tc.code)
				}
				if tc.want == 500 && body["error"] != "internal server error" {
					t.Fatalf("internal response = %s", w.Body)
				}
			}
			if tc.name == "list" && w.Body.String() != `{"items":[],"total":0,"limit":2,"offset":1}` {
				t.Fatalf("list response = %s", w.Body)
			}
			if tc.projectName != "" {
				var project domain.Project
				if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil || project.ID.String() != tc.id || project.Name != tc.projectName {
					t.Fatalf("body = %s, want project %s named %q", w.Body, tc.id, tc.projectName)
				}
			}
			if tc.want == 204 && w.Body.Len() != 0 {
				t.Fatalf("delete response must be empty, got %s", w.Body)
			}
		})
	}
}
