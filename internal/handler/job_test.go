package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/domain"
)

type jobHTTPService struct {
	JobService
	created bool
}

func (s jobHTTPService) Enqueue(_ context.Context, input domain.CreateJobInput) (domain.Job, bool, error) {
	token := uuid.New()
	return domain.Job{Type: input.Type, LeaseToken: &token}, s.created, nil
}

func (jobHTTPService) Cancel(_ context.Context, id string) (domain.Job, error) {
	if id == "missing" {
		return domain.Job{}, domain.ErrNotFound
	}
	return domain.Job{}, domain.ErrConflict
}

func TestJobHandler(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		created            bool
		status             int
	}{
		{"POST", "/jobs", `{"type":"mock.echo"}`, true, 202},
		{"POST", "/jobs", `{"type":"mock.echo"}`, false, 200},
		{"POST", "/jobs", `{"unknown":true}`, false, 400},
		{"POST", "/jobs", `{"type":"mock.echo"} {}`, false, 400},
		{"GET", "/jobs?limit=0", "", false, 400},
		{"GET", "/jobs?offset=-1", "", false, 400},
		{"POST", "/jobs/missing/cancel", "", false, 404},
		{"POST", "/jobs/finished/cancel", "", false, 409},
	} {
		engine := gin.New()
		NewJobHandler(jobHTTPService{created: tc.created}, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(engine.Group(""))
		resp := httptest.NewRecorder()
		engine.ServeHTTP(resp, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if resp.Code != tc.status || strings.Contains(resp.Body.String(), "lease_token") {
			t.Fatalf("%s %s = %d, body %s, want %d", tc.method, tc.path, resp.Code, resp.Body, tc.status)
		}
	}
}
