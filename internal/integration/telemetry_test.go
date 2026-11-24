package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"

	"projectlens/internal/domain"
	"projectlens/internal/handler"
	"projectlens/internal/router"
	"projectlens/internal/telemetry"
	"projectlens/internal/worker"
)

func TestMetricsAndTraceCorrelation(t *testing.T) {
	cfg, _, store, reg, svc := jobFixture(t)
	// This pool is closed deliberately to exercise a failed database scrape.
	pool := openPool(t, cfg)
	defer pool.Close()
	var logText bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logText, nil))
	if _, err := telemetry.StartMetrics("0.0.0.0:0", pool, log); err == nil {
		t.Fatal("accepted public listener")
	}
	stop, err := telemetry.StartMetrics("127.0.0.1:0", pool, log)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	var event struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(logText.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	const secret = "unique-private-marker"
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	const parentID = "00f067aa0ba902b7"
	finish := sync.OnceFunc(telemetry.StartTracing(true, log))
	defer finish()
	engine := router.New(cfg, log, router.Dependencies{Jobs: handler.NewJobHandler(svc, log)})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/jobs",
		strings.NewReader(fmt.Sprintf(`{"type":"mock.flaky","payload":{"succeed_on_attempt":2,"note":%q}}`, secret)))
	req.Header.Set("traceparent", "00-"+traceID+"-"+parentID+"-01")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue returned %d: %s", rec.Code, rec.Body.String())
	}
	var queued domain.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	job, err := store.GetByID(t.Context(), queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobParent := strings.Split(job.TraceParent, "-")
	if len(jobParent) != 4 || jobParent[1] != traceID || jobParent[2] == parentID {
		t.Fatalf("unexpected persisted traceparent %q", job.TraceParent)
	}
	func() {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		runner := worker.NewRunner(store, reg, cfg.Worker, log)
		go func() { defer close(done); runner.Run(ctx) }()
		defer func() { cancel(); <-done }()
		waitJob(t, store, job.ID, jobStatus(domain.JobSucceeded))
	}()
	finish()
	if _, ok := otel.GetTracerProvider().(noop.TracerProvider); !ok {
		t.Fatalf("tracer provider after cleanup = %T", otel.GetTracerProvider())
	}
	var httpSpan, workerSpan, attemptLog bool
	for _, line := range bytes.Split(bytes.TrimSpace(logText.Bytes()), []byte("\n")) {
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry["trace_id"] != traceID {
			continue
		}
		switch entry["name"] {
		case "http.request":
			httpSpan = entry["parent_id"] == parentID
		case "worker.execute":
			workerSpan = entry["parent_id"] == jobParent[2]
		}
		if entry["msg"] == "job attempt finished" && entry["job_id"] == job.ID.String() && entry["job_type"] == job.Type {
			workerID, _ := entry["worker_id"].(string)
			attemptLog = workerID != ""
		}
	}
	if !httpSpan || !workerSpan || !attemptLog {
		t.Fatalf("trace correlation missing: http span=%t, worker span=%t, attempt log=%t", httpSpan, workerSpan, attemptLog)
	}
	scrape := func() string {
		res, err := http.Get("http://" + event.Address + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		buf, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != 200 {
			t.Fatal(res.StatusCode, err)
		}
		return string(buf)
	}
	body := scrape()
	for _, key := range []string{"projectlens_jobs", "projectlens_job_retries_total", "projectlens_job_attempt_seconds_count", "projectlens_db_scrape_up 1"} {
		if !strings.Contains(body, key) {
			t.Fatal(key)
		}
	}
	for _, private := range []string{secret, job.ID.String(), traceID} {
		if strings.Contains(body, private) {
			t.Fatal("high cardinality or sensitive metric")
		}
	}
	pool.Close()
	if !strings.Contains(scrape(), "projectlens_db_scrape_up 0") {
		t.Fatal("missing failed scrape metric")
	}
}
