package router

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"projectlens/internal/config"
	"projectlens/internal/handler"
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
		{"/readyz", "status", "ok", http.StatusOK},
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
				resp.Header().Get("Access-Control-Allow-Origin") != "https://app.example" ||
				resp.Header().Get("X-Content-Type-Options") != "nosniff" || resp.Header().Get("X-Frame-Options") != "DENY" ||
				resp.Header().Get("Referrer-Policy") != "no-referrer" ||
				(tc.path == "/api/v1/ping" && resp.Header().Get("Cache-Control") != "no-store") {
				t.Fatalf("missing middleware headers: %v", resp.Header())
			}
			var body map[string]any
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body[tc.key] != tc.value {
				t.Fatalf("%s = %q, want %q", tc.key, body[tc.key], tc.value)
			}
		})
	}
}

type readinessPinger struct{ err error }

func (p readinessPinger) Ping(context.Context) error { return p.err }

func TestReadinessWithDatabase(t *testing.T) {
	cfg := config.Config{App: config.AppConfig{Env: "test"}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		err    error
		status int
	}{
		{nil, http.StatusOK},
		{errors.New("private database error"), http.StatusServiceUnavailable},
	} {
		engine := New(cfg, log, Dependencies{Pinger: readinessPinger{err: tc.err}})
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if w.Code != tc.status || strings.Contains(w.Body.String(), "private database error") {
			t.Errorf("readiness = %d %s, want %d", w.Code, w.Body, tc.status)
		}
	}
}

func TestRepositoryRoutes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := New(config.Config{App: config.AppConfig{Env: "test"}}, log, Dependencies{
		Projects:     handler.NewProjectHandler(nil, log),
		Repositories: handler.NewRepositoryHandler(nil, log),
		Snapshots:    handler.NewSnapshotHandler(nil, log),
		Jobs:         handler.NewJobHandler(nil, log),
	})
	base := "/api/v1/projects/:id/repositories"
	scoped := base + "/:repoId"
	want := map[string]bool{
		"POST " + base: true, "GET " + base: true, "GET " + base + "/:repoId": true,
		"PATCH " + scoped: true, "POST " + scoped + "/refresh-metadata": true, "POST " + scoped + "/sync": true, "DELETE " + scoped: true,
		"GET " + scoped + "/snapshots": true, "GET " + scoped + "/snapshots/active": true,
		"GET " + scoped + "/files": true, "GET " + scoped + "/files/content": true,
	}
	for _, route := range engine.Routes() {
		delete(want, route.Method+" "+route.Path)
	}
	if len(want) != 0 {
		t.Fatalf("missing routes: %v", want)
	}
}

func TestJobRoutes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := New(config.Config{App: config.AppConfig{Env: "test"}}, log, Dependencies{
		Jobs: handler.NewJobHandler(nil, log),
	})
	routes := make(map[string]bool)
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{"POST /api/v1/jobs", "GET /api/v1/jobs", "GET /api/v1/jobs/:id", "POST /api/v1/jobs/:id/cancel"} {
		if !routes[want] {
			t.Errorf("missing route %s", want)
		}
	}
}

func TestDocumentRoutes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := New(config.Config{App: config.AppConfig{Env: "test"}}, log, Dependencies{
		Projects:  handler.NewProjectHandler(nil, log),
		Documents: handler.NewDocumentHandler(nil, log, 0),
	})
	base := "/api/v1/projects/:id/documents"
	scoped := base + "/:docId"
	want := map[string]bool{
		"POST " + base: true, "GET " + base: true, "GET " + scoped: true,
		"PUT " + scoped: true, "PATCH " + scoped: true, "DELETE " + scoped: true,
	}
	for _, route := range engine.Routes() {
		delete(want, route.Method+" "+route.Path)
	}
	if len(want) != 0 {
		t.Fatalf("missing routes: %v", want)
	}
}

func TestSearchRoute(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := New(config.Config{App: config.AppConfig{Env: "test"}}, log, Dependencies{
		Projects: handler.NewProjectHandler(nil, log), Search: handler.NewSearchHandler(nil, log),
	})
	for _, route := range engine.Routes() {
		if route.Method == http.MethodGet && route.Path == "/api/v1/projects/:id/search" {
			return
		}
	}
	t.Fatal("project search route missing")
}
