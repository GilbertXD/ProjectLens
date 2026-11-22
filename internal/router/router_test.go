package router

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
	"projectlens/internal/middleware"
)

func TestRoutes(t *testing.T) {
	cfg := config.Config{
		App:  config.AppConfig{Name: "ProjectLens", Env: "test", Version: "0.1.0"},
		CORS: config.CORSConfig{AllowedOrigins: []string{"https://app.example"}},
	}
	engine := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if gin.Mode() != gin.TestMode {
		t.Fatalf("Gin mode = %q, want %q", gin.Mode(), gin.TestMode)
	}

	for _, tc := range []struct {
		path, key, value string
		status           int
	}{
		{"/healthz", "status", "ok", http.StatusOK},
		{"/api/v1/ping", "message", "pong", http.StatusOK},
		{"/missing", "error", "not found", http.StatusNotFound},
		{"/healthz/", "error", "not found", http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set(middleware.RequestIDHeader, "route-test")
			req.Header.Set("Origin", "https://app.example")
			resp := httptest.NewRecorder()
			engine.ServeHTTP(resp, req)

			if resp.Code != tc.status || !strings.HasPrefix(resp.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("status = %d, headers = %v, body = %s", resp.Code, resp.Header(), resp.Body)
			}
			if resp.Header().Get(middleware.RequestIDHeader) != "route-test" ||
				resp.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
				t.Fatalf("missing middleware headers: %v", resp.Header())
			}
			var body map[string]string
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body[tc.key] != tc.value {
				t.Fatalf("%s = %q, want %q", tc.key, body[tc.key], tc.value)
			}
		})
	}
}
