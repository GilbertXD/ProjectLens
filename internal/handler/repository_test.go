package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"projectlens/internal/domain"
)

type repositoryHandlerService struct {
	associate func(string, domain.AssociateRepositoryInput) (domain.Repository, error)
	list      func(string, domain.ListRepositoriesFilter) (domain.RepositoryListResult, error)
	get       func(string, string) (domain.Repository, error)
	patch     func(string, string, domain.PatchRepositoryInput) (domain.Repository, error)
	sync      func(string, string) (domain.Repository, error)
	delete    func(string, string) error
}

func (s repositoryHandlerService) ListByProject(_ context.Context, project string, filter domain.ListRepositoriesFilter) (domain.RepositoryListResult, error) {
	return s.list(project, filter)
}

func (s repositoryHandlerService) GetByID(_ context.Context, project, repo string) (domain.Repository, error) {
	return s.get(project, repo)
}

func (s repositoryHandlerService) Patch(_ context.Context, project, repo string, input domain.PatchRepositoryInput) (domain.Repository, error) {
	return s.patch(project, repo, input)
}

func (s repositoryHandlerService) Sync(_ context.Context, project, repo string) (domain.Repository, error) {
	return s.sync(project, repo)
}

func (s repositoryHandlerService) Delete(_ context.Context, project, repo string) error {
	return s.delete(project, repo)
}

func (s repositoryHandlerService) Associate(_ context.Context, project string, input domain.AssociateRepositoryInput) (domain.Repository, error) {
	return s.associate(project, input)
}

func TestRepositoryHandlerValidationAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, method, suffix, body string
		status                     int
		code                       string
		serviceErr                 error
	}{
		{"create", "POST", "", `{"source_type":"github","source":"https://github.com/owner/repo"}`, 201, "", nil},
		{"unknown", "POST", "", `{"extra":true}`, 400, "invalid_input", nil},
		{"multiple", "POST", "", `{} {}`, 400, "invalid_input", nil},
		{"null", "POST", "", `null`, 400, "invalid_input", nil},
		{"malformed", "POST", "", `{`, 400, "invalid_input", nil},
		{"patch immutable", "PATCH", "/repo", `{"source":"new"}`, 400, "invalid_input", nil},
		{"empty filter", "GET", "?source_type=", "", 400, "invalid_input", nil},
		{"zero limit", "GET", "?limit=0", "", 400, "invalid_input", nil},
		{"invalid offset", "GET", "?offset=text", "", 400, "invalid_input", nil},
		{"invalid", "POST", "", `{}`, 400, "invalid_input", domain.ErrInvalidInput},
		{"forbidden", "POST", "", `{}`, 403, "forbidden", domain.ErrForbidden},
		{"missing", "POST", "", `{}`, 404, "not_found", domain.ErrNotFound},
		{"conflict", "POST", "", `{}`, 409, "conflict", domain.ErrConflict},
		{"rate limit", "POST", "", `{}`, 429, "rate_limited", domain.ErrRateLimited},
		{"upstream", "POST", "", `{}`, 502, "upstream_error", domain.ErrUpstreamUnavailable},
		{"unexpected", "POST", "", `{}`, 500, "internal_error", errors.New("private details")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := repositoryHandlerService{associate: func(project string, input domain.AssociateRepositoryInput) (domain.Repository, error) {
				if tc.serviceErr != nil {
					return domain.Repository{}, fmt.Errorf("test: %w", tc.serviceErr)
				}
				if tc.status != 201 {
					t.Fatal("invalid body reached the service")
				}
				if project != "project" || input.SourceType != domain.RepositorySourceGitHub || input.Source != "https://github.com/owner/repo" {
					t.Fatalf("unexpected association call: %s %+v", project, input)
				}
				return domain.Repository{Name: "repo"}, nil
			}}
			engine := gin.New()
			NewRepositoryHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(engine.Group("/api/v1"))
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/v1/projects/project/repositories"+tc.suffix, strings.NewReader(tc.body)))
			if w.Code != tc.status || (tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`)) ||
				strings.Contains(w.Body.String(), "private details") {
				t.Fatalf("got %d %s, want %d %s", w.Code, w.Body, tc.status, tc.code)
			}
			if w.Code == http.StatusCreated && !strings.Contains(w.Body.String(), `"name":"repo"`) {
				t.Fatalf("unexpected creation response: %s", w.Body)
			}
		})
	}
}

func TestRepositoryHandlerManagement(t *testing.T) {
	checkIDs := func(project, repo string) {
		t.Helper()
		if project != "project" || repo != "repo" {
			t.Fatalf("unexpected route params %q %q", project, repo)
		}
	}
	service := repositoryHandlerService{
		list: func(project string, filter domain.ListRepositoriesFilter) (domain.RepositoryListResult, error) {
			want := domain.ListRepositoriesFilter{SourceType: domain.RepositorySourceGitHub, Role: domain.RepositoryRoleReference, Limit: 3, Offset: 6}
			if project != "project" || filter != want {
				t.Fatalf("unexpected list arguments %q %+v", project, filter)
			}
			return domain.RepositoryListResult{Items: []domain.Repository{}, Total: 5, Limit: 3, Offset: 6}, nil
		},
		get: func(project, repo string) (domain.Repository, error) {
			checkIDs(project, repo)
			return domain.Repository{Name: "original"}, nil
		},
		patch: func(project, repo string, input domain.PatchRepositoryInput) (domain.Repository, error) {
			checkIDs(project, repo)
			if input.Name == nil || *input.Name != "renamed" || input.Description != nil || input.Role == nil || *input.Role != domain.RepositoryRolePrimary {
				t.Fatalf("unexpected patch %+v", input)
			}
			return domain.Repository{Name: *input.Name}, nil
		},
		sync: func(project, repo string) (domain.Repository, error) {
			checkIDs(project, repo)
			return domain.Repository{DefaultBranch: "updated"}, nil
		},
		delete: func(project, repo string) error {
			checkIDs(project, repo)
			return nil
		},
	}
	engine := gin.New()
	NewRepositoryHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(engine.Group("/api/v1"))
	for _, tc := range []struct {
		method, suffix, body, fragment string
		status                         int
	}{
		{"GET", "?source_type=github&role=reference&limit=3&offset=6", "", `"total":5`, 200},
		{"GET", "?role=", "", `"code":"invalid_input"`, 400},
		{"GET", "?role=bad", "", `"code":"invalid_input"`, 400},
		{"GET", "?role=primary&role=reference", "", `"code":"invalid_input"`, 400},
		{"GET", "/repo", "", `"name":"original"`, 200},
		{"PATCH", "/repo", `{"name":"renamed","role":"primary"}`, `"name":"renamed"`, 200},
		{"POST", "/repo/refresh-metadata", "", `"default_branch":"updated"`, 200},
		{"DELETE", "/repo", "", "", 204},
	} {
		t.Run(tc.method+" "+tc.suffix, func(t *testing.T) {
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/v1/projects/project/repositories"+tc.suffix, strings.NewReader(tc.body)))
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.fragment) || (tc.status == 204 && w.Body.Len() != 0) {
				t.Fatalf("unexpected response %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestRepositoryHandlerErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		err     error
		message string
		status  int
	}{
		{domain.ErrNotFound, "project not found", 404},
		{fmt.Errorf("%w: repository not found", domain.ErrNotFound), "repository not found", 404},
		{fmt.Errorf("%w: GitHub repository not found", domain.ErrNotFound), "GitHub repository not found", 404},
		{domain.ErrConflict, "repository is already associated with this project", 409},
		{fmt.Errorf("%w: local repository path is outside configured roots", domain.ErrForbidden), "local repository path is outside configured roots", 403},
		{fmt.Errorf("%w: local repository path is unreadable", domain.ErrForbidden), "local repository path is unreadable", 403},
		{fmt.Errorf("%w: GitHub repository must be public", domain.ErrForbidden), "GitHub repository must be public", 403},
	} {
		t.Run(tc.message, func(t *testing.T) {
			service := repositoryHandlerService{sync: func(_, _ string) (domain.Repository, error) {
				return domain.Repository{}, tc.err
			}}
			engine := gin.New()
			NewRepositoryHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(engine.Group("/api/v1"))
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/projects/project/repositories/repo/refresh-metadata", nil))
			var response map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != tc.status || response["error"] != tc.message {
				t.Fatalf("unexpected response %d %s", w.Code, w.Body)
			}
		})
	}
}
