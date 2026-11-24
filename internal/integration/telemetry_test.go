package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"projectlens/internal/domain"
	"projectlens/internal/telemetry"
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
	finish := telemetry.StartTracing(true, log)
	ctx, span := telemetry.Span(t.Context(), "http.request")
	traceID := telemetry.TraceID(ctx)
	job, _, err := svc.Enqueue(ctx, domain.CreateJobInput{Type: "mock.flaky", Payload: []byte(fmt.Sprintf(`{"succeed_on_attempt":2,"note":%q}`, secret))})
	span.End()
	if err != nil || !strings.Contains(job.TraceParent, traceID) || traceID == "" {
		t.Fatalf("invalid trace propagation %v", err)
	}
	startJobWorker(t, cfg.Worker, store, reg)
	waitJob(t, store, job.ID, jobStatus(domain.JobSucceeded))
	finish()
	if !strings.Contains(logText.String(), `"name":"worker.execute","trace_id":"`+traceID) {
		t.Fatal("worker span not linked")
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
