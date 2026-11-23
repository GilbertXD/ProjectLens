package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/handler"
	"projectlens/internal/repository"
	"projectlens/internal/router"
	"projectlens/internal/service"
	"projectlens/internal/worker"
)

func jobFixture(t *testing.T) (config.Config, *pgxpool.Pool, *repository.PostgresJobStore, *worker.Registry, *service.JobService) {
	t.Helper()
	cfg := database(t)
	cfg.Worker = config.WorkerConfig{Concurrency: 2, PollInterval: 10 * time.Millisecond, LeaseDuration: 400 * time.Millisecond,
		HeartbeatInterval: 20 * time.Millisecond, RetryBaseDelay: 25 * time.Millisecond, RetryMaxDelay: 100 * time.Millisecond,
		DefaultTimeout: 5 * time.Second, DefaultMaxAttempts: 2, ShutdownTimeout: time.Second}
	pool := openPool(t, cfg)
	t.Cleanup(pool.Close)
	if err := db.MigrateUp(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	registry := &worker.Registry{}
	if err := worker.RegisterMockHandlers(registry); err != nil {
		t.Fatal(err)
	}
	store := repository.NewPostgresJobStore(pool)
	return cfg, pool, store, registry, service.NewJobService(store, registry, cfg.Worker)
}

func enqueueJob(t *testing.T, svc *service.JobService, typ, payload string, maxAttempts, timeout int) domain.Job {
	t.Helper()
	job, created, err := svc.Enqueue(t.Context(), domain.CreateJobInput{
		Type: typ, Payload: json.RawMessage(payload), MaxAttempts: &maxAttempts, TimeoutSeconds: &timeout,
	})
	if err != nil || !created {
		t.Fatalf("enqueue: created = %t, err = %v", created, err)
	}
	return job
}

func startJobWorker(t *testing.T, cfg config.WorkerConfig, store *repository.PostgresJobStore, registry *worker.Registry) {
	t.Helper()
	runner := worker.NewRunner(store, registry, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { runner.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitJob(t *testing.T, store *repository.PostgresJobStore, id uuid.UUID, condition func(domain.Job) bool) domain.Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := store.GetByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if condition(job) {
			return job
		}
		select {
		case <-ctx.Done():
			t.Fatalf("job %s did not reach expected state: %+v", id, job)
		case <-ticker.C:
		}
	}
}

func jobStatus(status domain.JobStatus) func(domain.Job) bool {
	return func(job domain.Job) bool { return job.Status == status }
}

func TestConcurrentJobClaims(t *testing.T) {
	_, _, store, _, svc := jobFixture(t)
	const jobs = 40
	for i := 0; i < jobs; i++ {
		enqueueJob(t, svc, "mock.echo", "{}", 2, 5)
	}
	claimed := make(chan domain.Job, jobs)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Go(func() {
			workerID := uuid.NewString()
			for {
				job, err := store.ClaimNext(t.Context(), workerID, uuid.New(), time.Minute)
				if errors.Is(err, domain.ErrNotFound) {
					return
				}
				if err != nil {
					t.Errorf("claim job: %v", err)
					return
				}
				claimed <- job
			}
		})
	}
	go func() {
		wg.Wait()
		close(claimed)
	}()
	seen := make(map[uuid.UUID]bool)
	for job := range claimed {
		if seen[job.ID] || job.Attempt != 1 || job.LeaseToken == nil || job.Status != domain.JobRunning {
			t.Fatalf("invalid or duplicate claim: %+v", job)
		}
		seen[job.ID] = true
	}
	if len(seen) != jobs {
		t.Fatalf("claimed %d jobs, want %d", len(seen), jobs)
	}
	if n, err := store.ReclaimExpiredLeases(t.Context(), jobs, 0); err != nil || n != 0 {
		t.Fatalf("reclaim active jobs: %d, %v", n, err)
	}
	if _, err := store.ClaimNext(t.Context(), "other", uuid.New(), time.Minute); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("claim while all leases active: %v", err)
	}
}

func TestJobLeaseRecovery(t *testing.T) {
	cfg, pool, store, registry, svc := jobFixture(t)
	retry := enqueueJob(t, svc, "mock.echo", `{"delay_ms":1500}`, 2, 5)
	failed := enqueueJob(t, svc, "mock.echo", "{}", 1, 5)
	cancelled := enqueueJob(t, svc, "mock.echo", "{}", 2, 5)
	tokens := make(map[uuid.UUID]uuid.UUID)
	for i := 0; i < 3; i++ {
		token := uuid.New()
		job, err := store.ClaimNext(t.Context(), "crashed", token, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		tokens[job.ID] = token
	}
	if _, err := svc.Cancel(t.Context(), cancelled.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE jobs SET lease_until = NOW() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.CompleteSucceeded(t.Context(), retry.ID, "crashed", tokens[retry.ID], nil); err != nil || ok {
		t.Fatalf("expired worker was able to complete before recovery: %t, %v", ok, err)
	}
	if n, err := store.ReclaimExpiredLeases(t.Context(), 3, 0); err != nil || n != 3 {
		t.Fatalf("reclaimed %d: %v", n, err)
	}
	for _, tc := range []struct {
		job    domain.Job
		status domain.JobStatus
		code   string
	}{
		{retry, domain.JobPending, "lease_expired"}, {failed, domain.JobFailed, "lease_expired"}, {cancelled, domain.JobCancelled, "cancelled"},
	} {
		job, err := store.GetByID(t.Context(), tc.job.ID)
		if err != nil || job.Status != tc.status || job.LastErrorCode == nil || *job.LastErrorCode != tc.code || job.LeaseToken != nil {
			t.Fatalf("reclaimed job = %+v, err = %v, want %s/%s", job, err, tc.status, tc.code)
		}
	}
	// Give the live runner the same identity to verify that the token alone fences out the old attempt.
	cfg.Worker.ID = "crashed"
	startJobWorker(t, cfg.Worker, store, registry)
	current := waitJob(t, store, retry.ID, jobStatus(domain.JobRunning))
	if current.Attempt != 2 || current.LeaseToken == nil || *current.LeaseToken == tokens[retry.ID] {
		t.Fatalf("reclaimed job: %+v", current)
	}
	if _, renewed, err := store.RenewLease(t.Context(), retry.ID, "crashed", tokens[retry.ID], time.Minute); err != nil || renewed {
		t.Fatalf("stale renewal: %t, %v", renewed, err)
	}
	if ok, err := store.CompleteSucceeded(t.Context(), retry.ID, "crashed", tokens[retry.ID], nil); err != nil || ok {
		t.Fatalf("stale success: %t, %v", ok, err)
	}
	if ok, err := store.CompleteFailed(t.Context(), retry.ID, "crashed", tokens[retry.ID], "stale", "stale", false, 0); err != nil || ok {
		t.Fatalf("stale failure: %t, %v", ok, err)
	}
	if job := waitJob(t, store, retry.ID, jobStatus(domain.JobSucceeded)); job.Attempt != 2 || len(job.Result) == 0 {
		t.Fatalf("recovered execution: %+v", job)
	}
}

func TestJobWorkerRetryTimeoutAndHeartbeat(t *testing.T) {
	cfg, _, store, registry, svc := jobFixture(t)
	for _, tc := range []struct {
		typ, payload      string
		timeout, attempts int
		status            domain.JobStatus
		code              string
	}{
		{"mock.fail", `{"error_message":"retry me"}`, 5, 2, domain.JobFailed, "handler_error"},
		{"mock.fail", `{"permanent":true}`, 5, 1, domain.JobFailed, "simulated_permanent_failure"},
		{"mock.flaky", `{"succeed_on_attempt":2}`, 5, 2, domain.JobSucceeded, ""},
		{"mock.sleep", `{"duration_ms":1400}`, 1, 2, domain.JobFailed, "timeout"},
		{"mock.sleep", `{"duration_ms":900}`, 5, 1, domain.JobSucceeded, ""},
	} {
		t.Run(tc.typ+"/"+tc.code, func(t *testing.T) {
			queued := enqueueJob(t, svc, tc.typ, tc.payload, 2, tc.timeout)
			startJobWorker(t, cfg.Worker, store, registry)
			if tc.status == domain.JobSucceeded && tc.typ == "mock.sleep" {
				running := waitJob(t, store, queued.ID, jobStatus(domain.JobRunning))
				waitJob(t, store, queued.ID, func(job domain.Job) bool {
					return job.Status == domain.JobRunning && job.LeaseUntil.After(*running.LeaseUntil)
				})
				if n, err := store.ReclaimExpiredLeases(t.Context(), 10, 0); err != nil || n != 0 {
					t.Fatalf("reclaimed healthy lease: %d, %v", n, err)
				}
			}
			job := waitJob(t, store, queued.ID, jobStatus(tc.status))
			if job.Attempt != tc.attempts {
				t.Fatalf("attempt = %d, want %d", job.Attempt, tc.attempts)
			}
			if tc.code != "" && (job.LastErrorCode == nil || *job.LastErrorCode != tc.code || job.LastError == nil || *job.LastError == "") {
				t.Fatalf("missing failure %q: %+v", tc.code, job)
			}
		})
	}
}

func TestJobAPIAndCancellation(t *testing.T) {
	cfg, pool, store, registry, svc := jobFixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(router.New(cfg, log, router.Dependencies{Jobs: handler.NewJobHandler(svc, log), Pinger: pool}))
	defer srv.Close()
	const path = "/api/v1/jobs"
	const payload = `{"type":"mock.sleep","payload":{"duration_ms":4000},"idempotency_key":"slow"}`
	var slow, replayed domain.Job
	started := time.Now()
	request(t, srv, http.MethodPost, path, payload, http.StatusAccepted, &slow)
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("enqueue without worker took %s", elapsed)
	}
	if slow.Status != domain.JobPending || slow.FinishedAt != nil {
		t.Fatalf("enqueue waited for execution: %+v", slow)
	}
	request(t, srv, http.MethodPost, path, payload, http.StatusOK, &replayed)
	if replayed.ID != slow.ID {
		t.Fatalf("replay ID = %s, want %s", replayed.ID, slow.ID)
	}
	request(t, srv, http.MethodPost, path, strings.Replace(payload, "4000", "3000", 1), http.StatusConflict, nil)
	request(t, srv, http.MethodGet, path+"/"+slow.ID.String(), "", http.StatusOK, &replayed)
	var list domain.JobListResult
	request(t, srv, http.MethodGet, path+"?type=mock.sleep&status=pending&offset=1", "", http.StatusOK, &list)
	if list.Total != 1 || len(list.Items) != 0 {
		t.Fatalf("list = %+v", list)
	}
	var pendingCancelled domain.Job
	pending := enqueueJob(t, svc, "mock.echo", "{}", 2, 5)
	request(t, srv, http.MethodPost, path+"/"+pending.ID.String()+"/cancel", "", http.StatusOK, &pendingCancelled)
	if pendingCancelled.Status != domain.JobCancelled || pendingCancelled.Attempt != 0 {
		t.Fatalf("pending cancel: %+v", pendingCancelled)
	}
	startJobWorker(t, cfg.Worker, store, registry)
	waitJob(t, store, slow.ID, jobStatus(domain.JobRunning))
	var live domain.Job
	started = time.Now()
	request(t, srv, http.MethodPost, path, strings.Replace(payload, `"slow"`, `"live"`, 1), http.StatusAccepted, &live)
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("enqueue with active worker took %s", elapsed)
	}
	if live.FinishedAt != nil {
		t.Fatalf("enqueue waited for execution: %+v", live)
	}
	var running domain.Job
	cancelPath := path + "/" + slow.ID.String() + "/cancel"
	request(t, srv, http.MethodPost, cancelPath, "", http.StatusOK, &running)
	if running.Status != domain.JobRunning || running.CancelRequestedAt == nil {
		t.Fatalf("running cancel: %+v", running)
	}
	cancelled := waitJob(t, store, slow.ID, jobStatus(domain.JobCancelled))
	request(t, srv, http.MethodPost, cancelPath, "", http.StatusOK, &replayed)
	if replayed.Status != domain.JobCancelled || !replayed.UpdatedAt.Equal(cancelled.UpdatedAt) {
		t.Fatalf("repeated cancel changed job: %+v", replayed)
	}
	for typ, status := range map[string]domain.JobStatus{"mock.echo": domain.JobSucceeded, "mock.fail": domain.JobFailed} {
		job := enqueueJob(t, svc, typ, "{}", 1, 5)
		waitJob(t, store, job.ID, jobStatus(status))
		request(t, srv, http.MethodPost, path+"/"+job.ID.String()+"/cancel", "", http.StatusConflict, nil)
	}
	request(t, srv, http.MethodPost, path+"/"+uuid.NewString()+"/cancel", "", http.StatusNotFound, nil)
}
