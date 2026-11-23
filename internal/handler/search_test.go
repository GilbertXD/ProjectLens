package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/domain"
)

type testSearchService func(context.Context, string, domain.SearchFilter) (domain.SearchResult, error)

func (s testSearchService) Search(ctx context.Context, id string, f domain.SearchFilter) (domain.SearchResult, error) {
	return s(ctx, id, f)
}

func searchTestRouter(s SearchService) *gin.Engine {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := gin.New()
	NewSearchHandler(s, log).Register(engine.Group("/api/v1"))
	return engine
}

func TestSearchHandlerParsesQueryAndWritesHeaders(t *testing.T) {
	repoID := uuid.New()
	want := domain.SearchFilter{Query: "\"find this\"", RepositoryID: &repoID, PathPrefix: "pkg/a_%", Limit: 8, Offset: 2,
		SourceTypes: []domain.SearchSourceType{domain.SearchSourceLocalGit, domain.SearchSourceGitHub, domain.SearchSourceDocument}}
	engine := searchTestRouter(testSearchService(func(_ context.Context, id string, filter domain.SearchFilter) (domain.SearchResult, error) {
		if id != "project-id" || !reflect.DeepEqual(filter, want) {
			t.Fatalf("id = %q, filter = %+v, want %+v", id, filter, want)
		}
		return domain.SearchResult{Query: filter.Query, Items: []domain.SearchHit{}, Limit: filter.Limit,
			Offset: filter.Offset, SearchVersion: 53, CacheStatus: domain.SearchCacheStatusHit}, nil
	}))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/projects/project-id/search?q=%22find+this%22&repository_id="+repoID.String()+
			"&path_prefix=pkg%2Fa_%25&limit=8&offset=2&source_type=local_git,github&source_type=document", nil))
	if response.Code != 200 || response.Header().Get("X-Search-Cache") != "hit" || response.Header().Get("X-Search-Version") != "53" {
		t.Fatalf("response = %d %v %s", response.Code, response.Header(), response.Body)
	}
	var result domain.SearchResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Items == nil ||
		result.Query != want.Query || result.Limit != want.Limit || result.Offset != want.Offset ||
		result.CacheStatus != domain.SearchCacheStatusHit || result.SearchVersion != 53 {
		t.Fatalf("response = %+v, %v", result, err)
	}
}

func TestSearchHandlerErrors(t *testing.T) {
	for _, query := range []string{
		"limit=1&limit=2", "offset=0&offset=1", "limit=0", "limit=", "limit=-1", "limit=1.5",
		"offset=-1", "offset=", "offset=1000000000000000000000", "repository_id=invalid",
		"q=a&q=b", "q=%ZZ", "path_prefix=a&path_prefix=b",
	} {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			searchTestRouter(nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/projects/id/search?"+query, nil))
			assertSearchHTTPError(t, w, 400, "invalid_input")
		})
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrInvalidInput, 400, "invalid_input"},
		{domain.ErrNotFound, 404, "not_found"},
		{errors.New("private SQL details"), 500, "internal_error"},
	} {
		engine := searchTestRouter(testSearchService(func(context.Context, string, domain.SearchFilter) (domain.SearchResult, error) {
			return domain.SearchResult{}, tc.err
		}))
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/projects/id/search?q=hello", nil))
		assertSearchHTTPError(t, w, tc.status, tc.code)
	}
}

func assertSearchHTTPError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != status || body["code"] != code ||
		body["error"] == "private SQL details" || w.Header().Get("X-Search-Cache") != "" || w.Header().Get("X-Search-Version") != "" {
		t.Fatalf("response = %d %v %s, want %d %s", w.Code, w.Header(), w.Body, status, code)
	}
}
