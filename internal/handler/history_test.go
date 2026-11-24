package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"projectlens/internal/domain"
)

func TestHistoryQueryValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	(&HistoryHandler{}).Register(engine.Group("/api/v1"))
	base := "/api/v1/projects/11111111-1111-1111-1111-111111111111/repositories/22222222-2222-2222-2222-222222222222"
	sha := strings.Repeat("a", 40)
	for _, path := range []string{
		"/refs?type=branch&type=tag",
		"/refs?type=other",
		"/refs?limit=0",
		"/refs?limit=101",
		"/refs?limit=",
		"/refs?limit=%2B1",
		"/refs?offset=-1",
		"/refs?offset=2147483648",
		"/refs?offset=1;limit=2",
		"/refs?unexpected=true",
		"/commits/" + sha + "/tree?path_prefix=dir&path_prefix=other",
		"/commits/" + sha + "/file?path=%ZZ",
		"/commits/" + sha + "/file",
		"/files/history?path=&ref=HEAD",
		"/files/history?path=a&ref=HEAD&%72ef=main",
		"/files/history?path=a&follow=TRUE",
		"/files/history?path=a&follow=1",
		"/files/history?path=a&follow=",
		"/files/blame?path=a&start_line=1",
		"/files/blame?path=a&end_line=1",
		"/files/blame?path=a&start_line=0&end_line=0",
		"/files/blame?path=a&start_line=2&end_line=1",
		"/files/blame?path=a&start_line=1&end_line=2001",
		"/files/blame?path=a&start_line=1&start_line=2&end_line=3",
	} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, base+path, nil))
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_input"`) {
				t.Fatalf("response = %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestHistoryQueryErrorResponses(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrNotFound, http.StatusNotFound, "not_found"},
		{domain.ErrInvalidInput, http.StatusBadRequest, "invalid_input"},
		{domain.ErrPayloadTooLarge, http.StatusRequestEntityTooLarge, "payload_too_large"},
		{domain.ErrRateLimited, http.StatusTooManyRequests, "rate_limited"},
		{domain.ErrUpstreamUnavailable, http.StatusBadGateway, "upstream_error"},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		(&RepositoryHandler{}).respond(c, http.StatusOK, nil, fmt.Errorf("%w: fixture", tc.err))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("error %v produced %d %s", tc.err, w.Code, w.Body)
		}
	}
}
