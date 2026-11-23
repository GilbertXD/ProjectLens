package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/domain"
)

type snapshotServiceStub struct {
	project, repo, path string
	search              domain.SearchFilesFilter
	list                domain.ListSnapshotsFilter
	call                string
	err                 error
}

func (s *snapshotServiceStub) record(method, project, repo string) {
	s.call, s.project, s.repo = method, project, repo
}

func (s *snapshotServiceStub) ListSnapshots(_ context.Context, project, repo string, filter domain.ListSnapshotsFilter) (domain.SnapshotListResult, error) {
	s.record("list", project, repo)
	s.list = filter
	return domain.SnapshotListResult{Items: []domain.Snapshot{}, Limit: filter.Limit, Offset: filter.Offset}, s.err
}

func (s *snapshotServiceStub) GetActiveSnapshot(_ context.Context, project, repo string) (domain.Snapshot, error) {
	s.record("active", project, repo)
	return domain.Snapshot{Status: domain.SnapshotStatusActive, Version: 2}, s.err
}

func (s *snapshotServiceStub) SearchActiveFiles(_ context.Context, project, repo string, filter domain.SearchFilesFilter) (domain.SourceFileListResult, error) {
	s.record("search", project, repo)
	s.search = filter
	return domain.SourceFileListResult{Items: []domain.SourceFile{{Path: "src/file.txt"}}, Total: 1, Limit: filter.Limit, Offset: filter.Offset}, s.err
}

func (s *snapshotServiceStub) GetActiveFile(_ context.Context, project, repo, path string) (domain.SourceFile, error) {
	s.record("detail", project, repo)
	s.path = path
	content := ""
	return domain.SourceFile{Path: path, Content: &content}, s.err
}

func TestSnapshotHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	project, repo := uuid.NewString(), uuid.NewString()
	base := "/api/v1/projects/" + project + "/repositories/" + repo
	for _, tc := range []struct {
		path, call, json string
		status           int
		err              error
	}{
		{"/snapshots?limit=5&offset=3", "list", `"items":[]`, 200, nil},
		{"/snapshots/active", "active", `"status":"active"`, 200, nil},
		{"/files?q=Find%25&path_prefix=src%2F&limit=5&offset=3", "search", `"path":"src/file.txt"`, 200, nil},
		{"/files/content?path=src%2Fa%2Bb.txt", "detail", `"content":""`, 200, nil},
		{"/snapshots?limit=0", "", `"code":"invalid_input"`, 400, nil},
		{"/snapshots?limit=101", "", `"code":"invalid_input"`, 400, nil},
		{"/files?offset=-1", "", `"code":"invalid_input"`, 400, nil},
		{"/files?offset=no", "", `"code":"invalid_input"`, 400, nil},
		{"/files/content", "", `"code":"invalid_input"`, 400, nil},
		{"/snapshots/active", "active", `"code":"not_found"`, 404, domain.ErrNotFound},
		{"/files/content?path=a", "detail", `"code":"not_found"`, 404, domain.ErrNotFound},
		{"/files", "search", `"code":"invalid_input"`, 400, domain.ErrInvalidInput},
		{"/snapshots/active", "active", `"code":"internal_error"`, 500, errors.New("private database error")},
	} {
		t.Run(tc.path, func(t *testing.T) {
			stub := &snapshotServiceStub{err: tc.err}
			engine := gin.New()
			NewSnapshotHandler(stub, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(engine.Group("/api/v1"))
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, base+tc.path, nil))
			body := w.Body.String()
			if w.Code != tc.status || !strings.Contains(body, tc.json) || strings.Contains(body, "private database error") || stub.call != tc.call {
				t.Fatalf("response = %d %s after %q, want %d %s after %q", w.Code, body, stub.call, tc.status, tc.json, tc.call)
			}
			if tc.status == http.StatusNotFound && !strings.Contains(body, `"error":"not found"`) {
				t.Fatalf("404 response must use a neutral error message, got %s", body)
			}
			if tc.call != "" && (stub.project != project || stub.repo != repo) {
				t.Fatalf("unexpected scope: %s, %s", stub.project, stub.repo)
			}
			if tc.err == nil {
				switch tc.call {
				case "list":
					if stub.list != (domain.ListSnapshotsFilter{Limit: 5, Offset: 3}) {
						t.Fatalf("list filter: %+v", stub.list)
					}
				case "search":
					want := domain.SearchFilesFilter{Query: "Find%", PathPrefix: "src/", Limit: 5, Offset: 3}
					if stub.search != want || strings.Contains(body, `"content"`) {
						t.Fatalf("search filter and result: %+v, %s", stub.search, body)
					}
				case "detail":
					if stub.path != "src/a+b.txt" {
						t.Fatalf("detail path: %q", stub.path)
					}
				}
			}
		})
	}
}
