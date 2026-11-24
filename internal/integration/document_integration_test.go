package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/repository"
)

func documentServer(t *testing.T, maxContentBytes int64) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	if testing.Short() {
		t.Skip("requires PostgreSQL and a loopback HTTP server")
	}
	cfg := database(t)
	cfg.Document.MaxContentBytes = maxContentBytes
	pool := openPool(t, cfg)
	t.Cleanup(pool.Close)
	if err := db.MigrateUp(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	srv := startHTTP(cfg, pool)
	t.Cleanup(srv.Close)
	return srv, pool
}

func createDocumentProject(t *testing.T, srv *httptest.Server, slug string) domain.Project {
	t.Helper()
	var project domain.Project
	request(t, srv, "POST", projects, fmt.Sprintf(`{"slug":%q,"name":%q}`, slug, slug), 201, &project)
	return project
}

type documentResponse struct {
	status int
	etag   string
	body   []byte
}

func sendDocumentRequest(ctx context.Context, srv *httptest.Server, method, path, body, ifMatch string) (documentResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		return documentResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		return documentResponse{}, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	return documentResponse{status: res.StatusCode, etag: res.Header.Get("ETag"), body: data}, err
}

func expectDocumentResponse(t *testing.T, srv *httptest.Server, method, path, body, ifMatch string, status int) documentResponse {
	t.Helper()
	res, err := sendDocumentRequest(t.Context(), srv, method, path, body, ifMatch)
	if err != nil || res.status != status {
		t.Fatalf("%s %s = %d (%s), error %v, want %d", method, path, res.status, res.body, err, status)
	}
	return res
}

func readDocument(t *testing.T, res documentResponse, version int64) domain.Document {
	t.Helper()
	var doc domain.Document
	if err := json.Unmarshal(res.body, &doc); err != nil || doc.Version != version || res.etag != fmt.Sprintf(`"%d"`, version) {
		t.Fatalf("document response = %s, ETag = %q, err = %v, want version %d", res.body, res.etag, err, version)
	}
	return doc
}

func expectDocumentCode(t *testing.T, res documentResponse, code, message string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(res.body, &body); err != nil || body["code"] != code || (message != "" && body["error"] != message) {
		t.Fatalf("response = %s, want code %s and message %q", res.body, code, message)
	}
}

func TestDocumentLifecycle(t *testing.T) {
	srv, pool := documentServer(t, 0)
	project := createDocumentProject(t, srv, "standalone-docs")
	base := projects + "/" + project.ID.String() + "/documents"
	var repoCount int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM repositories WHERE project_id = $1", project.ID).Scan(&repoCount); err != nil || repoCount != 0 {
		t.Fatalf("repository count = %d, %v", repoCount, err)
	}
	doc := readDocument(t, expectDocumentResponse(t, srv, "POST", base, `{"title":" Guide ","content":"# Hello"}`, "", 201), 1)
	if doc.ID == uuid.Nil || doc.ProjectID != project.ID || doc.Title != "Guide" || doc.ContentBytes != 7 || doc.CreatedAt.IsZero() || doc.UpdatedAt.IsZero() {
		t.Fatalf("created document = %+v", doc)
	}
	item := base + "/" + doc.ID.String()
	got := readDocument(t, expectDocumentResponse(t, srv, "GET", item, "", "", 200), 1)
	if got != doc {
		t.Fatalf("read = %+v, want %+v", got, doc)
	}
	second := readDocument(t, expectDocumentResponse(t, srv, "POST", base, `{"title":"Second"}`, "", 201), 1)
	if second.Content != "" || second.ContentBytes != 0 {
		t.Fatalf("empty document = %+v", second)
	}

	firstID, secondID := second.ID, doc.ID
	if doc.UpdatedAt.After(second.UpdatedAt) ||
		(doc.UpdatedAt.Equal(second.UpdatedAt) && doc.ID.String() > second.ID.String()) {
		firstID, secondID = secondID, firstID
	}
	for _, tc := range []struct {
		offset int
		id     uuid.UUID
	}{{0, firstID}, {1, secondID}, {2, uuid.Nil}} {
		res := expectDocumentResponse(t, srv, "GET", fmt.Sprintf("%s?limit=1&offset=%d", base, tc.offset), "", "", 200)
		var page domain.DocumentListResult
		if err := json.Unmarshal(res.body, &page); err != nil || page.Total != 2 || page.Limit != 1 || page.Offset != tc.offset ||
			page.Items == nil || strings.Contains(string(res.body), `"content":`) {
			t.Fatalf("page response = %s, %v", res.body, err)
		}
		if tc.id == uuid.Nil {
			if len(page.Items) != 0 {
				t.Fatalf("expected empty page, got %+v", page.Items)
			}
		} else if len(page.Items) != 1 || page.Items[0].ID != tc.id || page.Items[0].ProjectID != project.ID {
			t.Fatalf("unexpected page items = %+v", page.Items)
		}
	}

	got = readDocument(t, expectDocumentResponse(t, srv, "PUT", item, `{"title":"Full","content":"new","version":1}`, "", 200), 2)
	if got.Title != "Full" || got.Content != "new" || !got.CreatedAt.Equal(doc.CreatedAt) {
		t.Fatalf("PUT result = %+v", got)
	}
	got = readDocument(t, expectDocumentResponse(t, srv, "PATCH", item, `{"content":""}`, `W/"2"`, 200), 3)
	if got.Title != "Full" || got.Content != "" || got.ContentBytes != 0 {
		t.Fatalf("PATCH result = %+v", got)
	}
	origin := fmt.Sprintf(`{"origin":{"repository_id":%q,"repository_name":" Former Repo "}}`, uuid.New())
	got = readDocument(t, expectDocumentResponse(t, srv, "PATCH", item, origin, `"3"`, 200), 4)
	if got.Scope != domain.KnowledgeScopeProject || got.RepositoryID != nil || got.Origin == nil ||
		got.Origin.RepositoryName != "Former Repo" || got.Title != "Full" {
		t.Fatalf("PATCH origin result = %+v", got)
	}
	got = readDocument(t, expectDocumentResponse(t, srv, "PATCH", item, `{"origin":null}`, `"4"`, 200), 5)
	if got.Origin != nil || got.Scope != domain.KnowledgeScopeProject {
		t.Fatalf("PATCH clear origin result = %+v", got)
	}
	invalidOrigin := fmt.Sprintf(`{"origin":{"repository_id":%q,"repository_name":" "}}`, uuid.New())
	expectDocumentCode(t, expectDocumentResponse(t, srv, "PATCH", item, invalidOrigin, `"5"`, 400), "invalid_input", "")
	expectDocumentResponse(t, srv, "DELETE", item, "", "", 204)
	expectDocumentCode(t, expectDocumentResponse(t, srv, "GET", item, "", "", 404), "not_found", "document not found")
}

func TestDocumentProjectOwnership(t *testing.T) {
	srv, pool := documentServer(t, 0)
	owner := createDocumentProject(t, srv, "document-owner")
	other := createDocumentProject(t, srv, "other-project")
	ownerPath := projects + "/" + owner.ID.String()
	doc := readDocument(t, expectDocumentResponse(t, srv, "POST", ownerPath+"/documents", `{"title":"Private"}`, "", 201), 1)
	missingID := uuid.New()
	missing := projects + "/" + missingID.String() + "/documents"
	foreign := projects + "/" + other.ID.String() + "/documents"

	for _, tc := range []struct{ method, suffix, body string }{
		{"POST", "", `{"title":"Missing"}`},
		{"GET", "", ""},
		{"GET", "/" + doc.ID.String(), ""},
		{"PUT", "/" + doc.ID.String(), `{"title":"Updated","version":1}`},
		{"PATCH", "/" + doc.ID.String(), `{"content":"Changed","version":1}`},
		{"DELETE", "/" + doc.ID.String(), ""},
	} {
		expectDocumentCode(t, expectDocumentResponse(t, srv, tc.method, missing+tc.suffix, tc.body, "", 404), "not_found", "project not found")
		if tc.suffix != "" {
			expectDocumentCode(t, expectDocumentResponse(t, srv, tc.method, foreign+tc.suffix, tc.body, "", 404), "not_found", "document not found")
		}
	}
	var list domain.DocumentListResult
	request(t, srv, "GET", foreign, "", 200, &list)
	if list.Total != 0 || list.Items == nil || len(list.Items) != 0 {
		t.Fatalf("other project's list = %+v", list)
	}

	store := repository.NewPostgresDocumentStore(pool)
	if _, err := store.Create(t.Context(), domain.Document{ID: uuid.New(), ProjectID: missingID, Title: "Missing"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("store create missing project = %v", err)
	}
	request(t, srv, "DELETE", ownerPath, "", 204, nil)
	if _, err := store.GetByID(t.Context(), owner.ID, doc.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("store get after project deletion = %v", err)
	}
}

func TestDocumentContentLimits(t *testing.T) {
	const limit = 8
	srv, pool := documentServer(t, limit)
	project := createDocumentProject(t, srv, "document-limits")
	base := projects + "/" + project.ID.String() + "/documents"
	var doc domain.Document
	for _, content := range []string{"abcdefgh", "éééé"} {
		doc = readDocument(t, expectDocumentResponse(t, srv, "POST", base, fmt.Sprintf(`{"title":"Exact","content":%q}`, content), "", 201), 1)
		if doc.Content != content || doc.ContentBytes != limit {
			t.Fatalf("exact limit document = %+v", doc)
		}
	}
	reject := expectDocumentResponse(t, srv, "POST", base, `{"title":"Oversized","content":"ééééx"}`, "", 400)
	expectDocumentCode(t, reject, "invalid_input", "")
	item := base + "/" + doc.ID.String()
	for _, tc := range []struct{ method, body string }{
		{"PUT", `{"title":"Oversized","content":"ééééx"}`},
		{"PATCH", `{"content":"ééééx"}`},
	} {
		expectDocumentCode(t, expectDocumentResponse(t, srv, tc.method, item, tc.body, `"1"`, 400), "invalid_input", "")
	}
	got := readDocument(t, expectDocumentResponse(t, srv, "GET", item, "", "", 200), 1)
	if got.Content != doc.Content {
		t.Fatalf("rejected updates changed content to %q", got.Content)
	}

	const insert = "INSERT INTO documents (id, project_id, title, content) VALUES ($1, $2, 'Direct SQL', $3)"
	ceilingContent := strings.Repeat("é", 1<<19)
	var contentBytes int64
	err := pool.QueryRow(t.Context(), insert+" RETURNING content_bytes", uuid.New(), project.ID, ceilingContent).Scan(&contentBytes)
	if err != nil || contentBytes != 1<<20 {
		t.Fatalf("direct insert at ceiling = %d bytes, %v", contentBytes, err)
	}
	var pgErr *pgconn.PgError
	_, err = pool.Exec(t.Context(), insert, uuid.New(), project.ID, ceilingContent+"x")
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("direct over-limit insert = %v, want CHECK violation", err)
	}
}

func TestDocumentConcurrentVersionUpdates(t *testing.T) {
	srv, _ := documentServer(t, 0)
	project := createDocumentProject(t, srv, "document-concurrency")
	base := projects + "/" + project.ID.String() + "/documents"
	doc := readDocument(t, expectDocumentResponse(t, srv, "POST", base, `{"title":"Initial"}`, "", 201), 1)
	item := base + "/" + doc.ID.String()

	const writers = 10
	type writeResult struct {
		res   documentResponse
		err   error
		title string
	}
	start := make(chan struct{})
	results := make(chan writeResult, writers)
	for i := range writers {
		go func() {
			title := fmt.Sprintf("Writer %d", i)
			method := "PUT"
			if i%2 != 0 {
				method = "PATCH"
			}
			<-start
			res, err := sendDocumentRequest(t.Context(), srv, method, item, fmt.Sprintf(`{"title":%q,"content":%q,"version":1}`, title, title), "")
			results <- writeResult{res: res, err: err, title: title}
		}()
	}
	close(start)
	var winner string
	successes, conflicts := 0, 0
	for range writers {
		result := <-results
		if result.err != nil {
			t.Error(result.err)
			continue
		}
		switch result.res.status {
		case 200:
			successes++
			winner = result.title
			got := readDocument(t, result.res, 2)
			if got.Title != winner || got.Content != winner {
				t.Errorf("winning response = %+v, want %q", got, winner)
			}
		case 409:
			conflicts++
			expectDocumentCode(t, result.res, "version_conflict", "")
		default:
			t.Errorf("response = %d %s", result.res.status, result.res.body)
		}
	}
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("successes = %d, conflicts = %d", successes, conflicts)
	}
	got := readDocument(t, expectDocumentResponse(t, srv, "GET", item, "", "", 200), 2)
	if got.Title != winner || got.Content != winner {
		t.Fatalf("stored document = %+v, want %q", got, winner)
	}
	for _, tc := range []struct{ method, body string }{
		{"PUT", `{"title":"Retry","content":"Retry body"}`},
		{"PATCH", `{"title":"Retry"}`},
		{"DELETE", ""},
	} {
		expectDocumentCode(t, expectDocumentResponse(t, srv, tc.method, item, tc.body, `"1"`, 409), "version_conflict", "")
	}
	got = readDocument(t, expectDocumentResponse(t, srv, "PATCH", item, `{"title":"Retry"}`, `"2"`, 200), 3)
	if got.Title != "Retry" || got.Content != winner {
		t.Fatalf("retry result = %+v", got)
	}
	expectDocumentResponse(t, srv, "DELETE", item, "", `"3"`, 204)
}
