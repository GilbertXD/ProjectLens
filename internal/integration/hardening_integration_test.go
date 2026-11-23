package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/domain"
	"projectlens/internal/gitrepo"
	"projectlens/internal/handler"
	"projectlens/internal/logger"
	"projectlens/internal/router"
	"projectlens/internal/service"
)

type hardeningProjects struct{ handler.ProjectService }

func (hardeningProjects) GetByID(ctx context.Context, _ string) (domain.Project, error) {
	<-ctx.Done()
	return domain.Project{}, fmt.Errorf("database query: %w", ctx.Err())
}

type existingProject struct{}

func (existingProject) GetByID(_ context.Context, id uuid.UUID) (domain.Project, error) {
	return domain.Project{ID: id}, nil
}

func TestHTTPHardeningIntegration(t *testing.T) {
	const primary = "primary-token-fixture"
	const rotation = "rotation-token-fixture"
	const origin = "https://app.example"
	cfg := config.Config{
		App:      config.AppConfig{Env: "test"},
		Auth:     config.AuthConfig{Enabled: true, BearerTokens: []string{primary, rotation}},
		Server:   config.ServerConfig{MaxBodyBytes: 512 << 10, MaxInFlight: 1, RequestTimeout: 250 * time.Millisecond},
		CORS:     config.CORSConfig{AllowedOrigins: []string{origin}, AllowedHeaders: []string{"Authorization"}, AllowedMethods: []string{"GET", "OPTIONS"}},
		Document: config.DocumentConfig{MaxContentBytes: 8},
	}
	var output bytes.Buffer
	log := logger.New(config.LogConfig{Level: "info", Format: "json"}, &output)
	localRoots := gitrepo.NewLocalInspector([]string{t.TempDir()})
	docs := service.NewDocumentService(existingProject{}, nil, cfg.Document.MaxContentBytes)
	repos := service.NewRepositoryService(existingProject{}, nil, localRoots, nil)
	engine := router.New(cfg, log, router.Dependencies{
		Projects:     handler.NewProjectHandler(hardeningProjects{}, log),
		Jobs:         handler.NewJobHandler(nil, log),
		Documents:    handler.NewDocumentHandler(docs, log, cfg.Document.MaxContentBytes),
		Repositories: handler.NewRepositoryHandler(repos, log),
	})
	engine.GET("/timeout", func(c *gin.Context) { <-c.Request.Context().Done() })
	engine.GET("/panic", func(*gin.Context) { panic(primary) })
	entered := make(chan struct{})
	release := make(chan struct{})
	engine.GET("/held", func(c *gin.Context) {
		close(entered)
		<-release
		c.Status(http.StatusNoContent)
	})

	serve := func(method, path, body string, want int, auth ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Origin", origin)
		req.Header.Set("X-Request-ID", "hardening-fixture")
		req.Header.Set("User-Agent", "hardening-suite")
		req.Header.Set("Cookie", "session=cookie-fixture")
		for _, value := range auth {
			req.Header.Add("Authorization", value)
		}
		res := httptest.NewRecorder()
		engine.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %.200s", method, path, res.Code, want, res.Body.String())
		}
		return res
	}
	const ping = "/api/v1/ping"
	for _, auth := range [][]string{nil, {"Basic " + primary}, {"Bearer"}, {"Bearer invalid-token-fixture"},
		{"Bearer " + primary + " another"}, {"Bearer " + primary, "Bearer " + rotation}} {
		res := serve("GET", ping, "", 401, auth...)
		assertHardeningCode(t, res, "unauthorized")
		if res.Header().Get("WWW-Authenticate") != `Bearer realm="ProjectLens"` {
			t.Fatal("missing Bearer challenge")
		}
	}
	serve("GET", ping+"?value=query-fixture", "", 200, "bEaReR\t"+primary)
	serve("GET", ping, "", 200, "Bearer "+rotation)
	for _, path := range []string{"/healthz", "/readyz", "/openapi.yaml", "/openapi.json"} {
		serve("GET", path, "", 200)
	}
	preflight := serve("OPTIONS", ping, "", 204)
	if preflight.Header().Get("Access-Control-Allow-Origin") != origin ||
		preflight.Header().Get("Access-Control-Allow-Headers") != "Authorization" {
		t.Fatal("preflight omitted CORS headers")
	}
	bearer := "Bearer " + primary
	const project = projects + "/00000000-0000-0000-0000-000000000001"
	assertHardeningCode(t, serve("POST", project+"/repositories", `{"source_type":"local_git","source":"/outside/allowlist"}`, 403, bearer), "forbidden")
	document := fmt.Sprintf(`{"title":"doc","content":%q}`, strings.Repeat("x", 270<<10))
	assertHardeningCode(t, serve("POST", project+"/documents", document, 413, bearer), "payload_too_large")

	huge := strings.Repeat("x", 513<<10)
	assertHardeningCode(t, serve("POST", projects, huge, 413, bearer), "payload_too_large")
	for _, path := range []string{projects, project + "/repositories", project + "/documents", "/api/v1/jobs"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`+strings.Repeat(" ", 513<<10)))
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
		req.Header.Set("Authorization", bearer)
		res := httptest.NewRecorder()
		engine.ServeHTTP(res, req)
		if res.Code != 413 {
			t.Fatalf("chunked %s status = %d", path, res.Code)
		}
		assertHardeningCode(t, res, "payload_too_large")
	}
	assertHardeningCode(t, serve("GET", "/timeout", "", 504), "request_timeout")
	assertHardeningCode(t, serve("GET", project, "", 504, bearer), "request_timeout")
	assertHardeningCode(t, serve("GET", "/panic", "", 500), "internal_error")
	serve("GET", ping, "", 200, bearer)

	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest("GET", "/held", nil))
		done <- w.Code
	}()
	defer func() {
		close(release)
		if status := <-done; status != http.StatusNoContent {
			t.Errorf("accepted request returned %d", status)
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("held request did not enter")
	}
	busy := serve("GET", ping, "", 503, bearer)
	assertHardeningCode(t, busy, "service_unavailable")
	if busy.Header().Get("Retry-After") != "1" {
		t.Fatal("missing Retry-After")
	}
	serve("GET", "/healthz", "", 200)
	serve("GET", "/readyz", "", 200)

	for _, secret := range []string{primary, rotation, "invalid-token-fixture", "cookie-fixture", "query-fixture"} {
		if strings.Contains(output.String(), secret) {
			t.Fatal("access or panic log contains credential or query data")
		}
	}
	levels := map[string]string{}
	authenticated := false
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "http request" {
			continue
		}
		for _, key := range []string{"request_id", "method", "path", "route", "status", "latency_ms", "bytes_out", "bytes_in", "client_ip", "user_agent", "auth"} {
			if _, ok := record[key]; !ok {
				t.Errorf("access record missing %s", key)
			}
		}
		if record["path"] == ping && record["status"] == float64(200) && record["auth"] == "authenticated" {
			authenticated = true
		}
		if record["status"] == float64(401) && record["auth"] != "rejected" {
			t.Error("401 log missing rejected auth state")
		}
		levels[fmt.Sprint(record["status"])] = fmt.Sprint(record["level"])
	}
	if !authenticated || levels["200"] != "INFO" || levels["401"] != "WARN" || levels["500"] != "ERROR" {
		t.Fatalf("unexpected levels or auth state: %+v", levels)
	}
}

func assertHardeningCode(t *testing.T, res *httptest.ResponseRecorder, code string) {
	t.Helper()
	var body struct {
		Code      string
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || body.Code != code ||
		(body.RequestID != "" && body.RequestID != res.Header().Get("X-Request-ID")) {
		t.Fatalf("expected error code %q and consistent request ID: %s", code, res.Body.String())
	}
}

func TestHardeningWithPostgres(t *testing.T) {
	cfg := database(t)
	cfg.Auth = config.AuthConfig{Enabled: true, BearerTokens: []string{"postgres-hardening-token"}}
	pool := openPool(t, cfg)
	defer pool.Close()
	ping := router.New(cfg, logger.New(config.LogConfig{}, &bytes.Buffer{}), router.Dependencies{Pinger: pool})
	check := func(status int, postgres string) {
		res := httptest.NewRecorder()
		ping.ServeHTTP(res, httptest.NewRequest("GET", "/readyz", nil))
		if res.Code != status || !strings.Contains(res.Body.String(), `"postgres":"`+postgres+`"`) ||
			!strings.Contains(res.Body.String(), `"redis":"disabled"`) {
			t.Fatalf("readiness = %d %s", res.Code, res.Body.String())
		}
	}
	check(200, "ok")
	pool.Close()
	check(503, "unavailable")
}
