package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/github"
	"projectlens/internal/gitrepo"
	"projectlens/internal/handler"
	"projectlens/internal/repository"
	"projectlens/internal/router"
	"projectlens/internal/service"
)

func database(t *testing.T) config.Config {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is unset")
	}

	cfg := config.Config{
		App:    config.AppConfig{Env: "test"},
		Search: config.DefaultSearchConfig(),
		Database: config.DatabaseConfig{
			DSN: dsn, MaxConns: 5, MinConns: 0, MaxConnLifetime: 30 * time.Minute,
			MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: time.Minute, ConnectTimeout: 5 * time.Second,
		},
	}
	admin := openPool(t, cfg)
	t.Cleanup(admin.Close)
	schema := "projectlens_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	// Append a libpq setting to keyword DSNs and a query parameter to URL DSNs.
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		uri, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := uri.Query()
		query.Set("search_path", schema)
		uri.RawQuery = query.Encode()
		cfg.Database.DSN = uri.String()
	} else {
		cfg.Database.DSN += " search_path=" + schema
	}
	return cfg
}

func openPool(t *testing.T, cfg config.Config) *pgxpool.Pool {
	t.Helper()
	pool, err := db.NewPool(t.Context(), cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func startHTTP(cfg config.Config, pool *pgxpool.Pool, optionalCache ...service.SearchCache) *httptest.Server {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := repository.NewPostgresProjectRepository(pool)
	svc := service.NewProjectService(store, db.NewPgxTxManager(pool))
	repos := service.NewRepositoryService(store, repository.NewPostgresRepositoryStore(pool),
		gitrepo.NewLocalInspector(cfg.Repository.AllowedLocalRoots), github.NewClient(cfg.GitHub, nil))
	documents := service.NewDocumentService(store, repository.NewPostgresDocumentStore(pool), cfg.Document.MaxContentBytes)
	var searchCache service.SearchCache
	if len(optionalCache) > 0 {
		searchCache = optionalCache[0]
	}
	search := service.NewSearchService(repository.NewPostgresSearchStore(pool), searchCache, cfg.Search, cfg.Redis.KeyPrefix, log)
	return httptest.NewServer(router.New(cfg, log, router.Dependencies{
		Search:    handler.NewSearchHandler(search, log),
		Documents: handler.NewDocumentHandler(documents, log, cfg.Document.MaxContentBytes),
		Projects:  handler.NewProjectHandler(svc, log), Repositories: handler.NewRepositoryHandler(repos, log), Pinger: pool,
	}))
}

func request(t *testing.T, srv *httptest.Server, method, path, body string, wantStatus int, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != wantStatus {
		data, _ := io.ReadAll(res.Body)
		t.Fatalf("%s %s = %d, want %d. Body: %s", method, path, res.StatusCode, wantStatus, data)
	}
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}

const projects = "/api/v1/projects"

func TestProjectLifecycle(t *testing.T) {
	cfg := database(t)
	pool := openPool(t, cfg)
	defer pool.Close()
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateDown(ctx, pool); err != nil {
		t.Fatalf("migrate unified search down: %v", err)
	}
	var searchRemoved bool
	if err := pool.QueryRow(ctx, `SELECT
 to_regclass('documents') IS NOT NULL AND to_regclass('source_files') IS NOT NULL AND
 to_regclass('snapshots') IS NOT NULL AND to_regclass('jobs') IS NOT NULL AND
 to_regclass('repositories') IS NOT NULL AND to_regclass('projects') IS NOT NULL AND
 to_regclass('project_audit_logs') IS NOT NULL AND
 to_regclass('idx_source_files_search_vector') IS NULL AND to_regclass('idx_documents_search_vector') IS NULL AND
 to_regprocedure('bump_repository_search_version()') IS NULL AND to_regprocedure('bump_snapshot_search_version()') IS NULL AND
 to_regprocedure('bump_document_search_version()') IS NULL AND
 NOT EXISTS (SELECT FROM information_schema.columns WHERE table_schema = current_schema()
     AND ((table_name IN ('documents', 'source_files') AND column_name = 'search_vector')
          OR (table_name = 'projects' AND column_name = 'search_version'))) AND
 NOT EXISTS (SELECT FROM information_schema.triggers WHERE trigger_schema = current_schema()
     AND trigger_name IN ('documents_search_version', 'repositories_search_version', 'snapshots_search_version'))`).Scan(&searchRemoved); err != nil || !searchRemoved {
		t.Fatalf("unified search migration down did not preserve prior tables: removed=%t, error=%v", searchRemoved, err)
	}
	if err := db.MigrateDown(ctx, pool); err != nil {
		t.Fatalf("migrate documents down: %v", err)
	}
	var documentsRemoved bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('documents') IS NULL
 AND to_regclass('source_files') IS NOT NULL AND to_regclass('snapshots') IS NOT NULL AND to_regclass('jobs') IS NOT NULL
 AND to_regclass('repositories') IS NOT NULL AND to_regclass('projects') IS NOT NULL`).Scan(&documentsRemoved); err != nil || !documentsRemoved {
		t.Fatalf("documents migration down did not preserve prior tables: removed=%t, error=%v", documentsRemoved, err)
	}
	if err := db.MigrateDown(ctx, pool); err != nil {
		t.Fatalf("migrate snapshots down: %v", err)
	}
	var snapshotsRemoved bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('source_files') IS NULL AND to_regclass('snapshots') IS NULL
 AND to_regclass('jobs') IS NOT NULL AND to_regclass('repositories') IS NOT NULL AND to_regclass('projects') IS NOT NULL`).Scan(&snapshotsRemoved); err != nil || !snapshotsRemoved {
		t.Fatalf("snapshots migration down did not preserve prior tables: removed=%t, error=%v", snapshotsRemoved, err)
	}
	if err := db.MigrateDown(ctx, pool); err != nil {
		t.Fatalf("migrate jobs down: %v", err)
	}
	var jobsRemoved bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('jobs') IS NULL AND to_regclass('repositories') IS NOT NULL").Scan(&jobsRemoved); err != nil || !jobsRemoved {
		t.Fatalf("jobs migration down did not preserve repositories: removed=%t, error=%v", jobsRemoved, err)
	}
	if err := db.MigrateDown(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var removed bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('repositories') IS NULL AND to_regclass('projects') IS NOT NULL").Scan(&removed); err != nil || !removed {
		t.Fatalf("repository migration down did not preserve projects: removed=%t, error=%v", removed, err)
	}
	if err := db.MigrateDown(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT to_regclass('projects') IS NULL AND to_regclass('project_audit_logs') IS NULL").Scan(&removed); err != nil || !removed {
		t.Fatalf("migration down left tables: removed=%t, error=%v", removed, err)
	}
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}

	srv := startHTTP(cfg, pool)
	defer srv.Close()
	request(t, srv, "GET", "/readyz", "", 200, nil)
	var project, other, got domain.Project
	request(t, srv, "POST", projects, `{"slug":" Alpha-Project ","name":" Alpha "}`, 201, &project)
	if project.Slug != "alpha-project" || project.Name != "Alpha" || project.Status != domain.ProjectStatusActive || project.ID == uuid.Nil {
		t.Fatalf("unexpected project: %+v", project)
	}
	path := projects + "/" + project.ID.String()
	request(t, srv, "GET", path, "", 200, &got)
	if got != project {
		t.Fatalf("GET = %+v, want %+v", got, project)
	}
	request(t, srv, "POST", projects, `{"slug":"second-project","name":"Second"}`, 201, &other)
	request(t, srv, "POST", projects, `{"slug":"ALPHA-PROJECT","name":"Duplicate"}`, 409, nil)
	request(t, srv, "PUT", path, `{"slug":"second-project","name":"Conflict","status":"active"}`, 409, nil)
	request(t, srv, "PATCH", path, `{"status":"archived"}`, 200, &got)
	if got.Status != domain.ProjectStatusArchived || got.Name != project.Name || got.Slug != project.Slug {
		t.Fatalf("unexpected patched project: %+v", got)
	}
	var list domain.ProjectListResult
	request(t, srv, "GET", projects+"?status=archived&limit=1&offset=0", "", 200, &list)
	if list.Total != 1 || list.Limit != 1 || list.Offset != 0 || len(list.Items) != 1 || list.Items[0] != got {
		t.Fatalf("unexpected filtered page: %+v", list)
	}
	request(t, srv, "GET", projects+"?status=archived&limit=1&offset=1", "", 200, &list)
	if list.Total != 1 || list.Offset != 1 || list.Items == nil || len(list.Items) != 0 {
		t.Fatalf("unexpected empty page: %+v", list)
	}
	request(t, srv, "GET", projects+"?limit=0", "", 400, nil)
	request(t, srv, "GET", projects+"?offset=-1", "", 400, nil)
	request(t, srv, "GET", projects+"?status=unknown", "", 400, nil)
	request(t, srv, "GET", projects+"/invalid", "", 400, nil)
	request(t, srv, "PATCH", path, `{}`, 400, nil)
	request(t, srv, "POST", projects, `{"slug":"bad_slug","name":"Bad"}`, 400, nil)
	request(t, srv, "PUT", path, `{"slug":"alpha-project","name":"Replaced","description":"New","status":"active"}`, 200, &got)
	if got.Name != "Replaced" || got.Description != "New" || got.Status != domain.ProjectStatusActive || got.ID != project.ID {
		t.Fatalf("unexpected replaced project: %+v", got)
	}
	request(t, srv, "DELETE", path, "", 204, nil)
	request(t, srv, "GET", path, "", 404, nil)

	repo := repository.NewPostgresProjectRepository(pool)
	logs, err := repo.ListAuditLogsBySlug(ctx, project.Slug)
	actions := []string{"deleted", "updated", "updated", "created"}
	if err != nil || len(logs) != len(actions) {
		t.Fatalf("audit logs = %+v, error = %v", logs, err)
	}
	for i, entry := range logs {
		if entry.Action != actions[i] || entry.ProjectID != nil {
			t.Errorf("unexpected audit entry: %+v", entry)
		}
	}
}

type invalidAuditRepository struct {
	*repository.PostgresProjectRepository
}

func (r invalidAuditRepository) InsertAuditLog(ctx context.Context, entry domain.ProjectAuditLog) error {
	entry.Action = "invalid"
	return r.PostgresProjectRepository.InsertAuditLog(ctx, entry)
}

func TestProjectAuditFailureRollsBack(t *testing.T) {
	cfg := database(t)
	pool := openPool(t, cfg)
	defer pool.Close()
	if err := db.MigrateUp(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	repo := invalidAuditRepository{repository.NewPostgresProjectRepository(pool)}
	svc := service.NewProjectService(repo, db.NewPgxTxManager(pool))
	_, err := svc.Create(t.Context(), domain.CreateProjectInput{Slug: "rolled-back", Name: "Rolled back"})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected audit constraint failure, got %v", err)
	}
	list, err := svc.List(t.Context(), domain.ListProjectsFilter{})
	if err != nil || list.Total != 0 {
		t.Fatalf("write survived rollback: %+v, error = %v", list, err)
	}
	logs, err := repo.ListAuditLogsBySlug(t.Context(), "rolled-back")
	if err != nil || len(logs) != 0 {
		t.Fatalf("audit logs = %+v, error = %v", logs, err)
	}
}

func TestProjectPersistsAcrossRestart(t *testing.T) {
	cfg := database(t)
	var created domain.Project
	func() {
		pool := openPool(t, cfg)
		defer pool.Close()
		if err := db.MigrateUp(t.Context(), pool); err != nil {
			t.Fatal(err)
		}
		srv := startHTTP(cfg, pool)
		defer srv.Close()
		request(t, srv, "POST", projects, `{"slug":"persistent","name":"Persistent","description":"Across restart"}`, 201, &created)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := srv.Config.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	}()

	pool := openPool(t, cfg)
	defer pool.Close()
	if err := db.MigrateUp(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	srv := startHTTP(cfg, pool)
	defer srv.Close()
	var got domain.Project
	request(t, srv, "GET", projects+"/"+created.ID.String(), "", 200, &got)
	if got != created {
		t.Fatalf("after restart = %+v, want %+v", got, created)
	}
}
