package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

type workerTestStore struct {
	Store
	renew   func() (bool, bool, error)
	outcome chan string
}

func (s *workerTestStore) RenewLease(context.Context, uuid.UUID, string, uuid.UUID, time.Duration) (bool, bool, error) {
	return s.renew()
}
func (s *workerTestStore) CompleteSucceeded(context.Context, uuid.UUID, string, uuid.UUID, json.RawMessage) (bool, error) {
	s.outcome <- "succeeded"
	return false, nil
}
func (s *workerTestStore) CompleteFailed(_ context.Context, _ uuid.UUID, _ string, _ uuid.UUID, code, _ string, retryable bool, _ time.Duration) (bool, error) {
	if retryable {
		code += ":retry"
	}
	s.outcome <- code
	return true, nil
}
func (s *workerTestStore) MarkCancelled(context.Context, uuid.UUID, string, uuid.UUID, string) (bool, error) {
	s.outcome <- "cancelled"
	return true, nil
}

func TestExecutionOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name               string
		handler            HandlerFunc
		requested, renewed bool
		want               []string
	}{
		{name: "panic", handler: func(context.Context, domain.Job) (json.RawMessage, error) { panic("broken handler") }, want: []string{"panic"}},
		{name: "unknown", want: []string{"unknown_job_type"}},
		{name: "success races cancel", handler: func(context.Context, domain.Job) (json.RawMessage, error) { return []byte("{}"), nil }, renewed: true, want: []string{"succeeded", "cancelled"}},
		{name: "timeout", handler: func(context.Context, domain.Job) (json.RawMessage, error) { return nil, context.DeadlineExceeded }, renewed: true, want: []string{"timeout:retry"}},
		{name: "cancel requested", handler: blockingJob, requested: true, renewed: true, want: []string{"cancelled"}},
		{name: "lost lease", handler: blockingJob},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &workerTestStore{outcome: make(chan string, 4), renew: func() (bool, bool, error) { return tc.requested, tc.renewed, nil }}
			registry := &Registry{}
			if tc.handler != nil {
				if err := registry.Register("job", tc.handler); err != nil {
					t.Fatal(err)
				}
			}
			runner := NewRunner(store, registry, config.WorkerConfig{LeaseDuration: time.Second, HeartbeatInterval: 20 * time.Millisecond,
				RetryBaseDelay: time.Second, RetryMaxDelay: time.Minute}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			runner.execute(t.Context(), domain.Job{ID: uuid.New(), Type: "job", TimeoutSeconds: 2, Attempt: 1}, uuid.New())
			close(store.outcome)
			var got []string
			for outcome := range store.outcome {
				got = append(got, outcome)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("outcomes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("outcomes = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func blockingJob(ctx context.Context, _ domain.Job) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRetryDelay(t *testing.T) {
	r := Runner{cfg: config.WorkerConfig{RetryBaseDelay: 2 * time.Second, RetryMaxDelay: 7 * time.Second}}
	for attempt, want := range map[int]time.Duration{1: 2 * time.Second, 2: 4 * time.Second, 3: 7 * time.Second, 100: 7 * time.Second} {
		if got := r.retryDelay(attempt); got != want {
			t.Errorf("attempt %d: delay = %s, want %s", attempt, got, want)
		}
	}
}

type drainStore struct {
	workerTestStore
	claimed bool
}

func (s *drainStore) ClaimNext(_ context.Context, _ string, _ uuid.UUID, _ time.Duration) (domain.Job, error) {
	if s.claimed {
		return domain.Job{}, domain.ErrNotFound
	}
	s.claimed = true
	return domain.Job{ID: uuid.New(), Type: "drain", TimeoutSeconds: 5, Attempt: 1}, nil
}

func (*drainStore) ReclaimExpiredLeases(context.Context, int, time.Duration) (int64, error) {
	return 0, nil
}

func TestShutdownDrainsOrAborts(t *testing.T) {
	for _, forced := range []bool{false, true} {
		name := "drain"
		if forced {
			name = "abort"
		}
		t.Run(name, func(t *testing.T) {
			started, renewed, release, done := make(chan struct{}), make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
			store := &drainStore{workerTestStore: workerTestStore{
				outcome: make(chan string, 4),
				renew: func() (bool, bool, error) {
					select {
					case renewed <- struct{}{}:
					default:
					}
					return false, true, nil
				},
			}}
			registry := &Registry{}
			if err := registry.Register("drain", func(ctx context.Context, _ domain.Job) (json.RawMessage, error) {
				close(started)
				select {
				case <-release:
					return []byte("{}"), nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}); err != nil {
				t.Fatal(err)
			}
			cfg := config.WorkerConfig{Concurrency: 1, PollInterval: 10 * time.Millisecond, LeaseDuration: time.Second,
				HeartbeatInterval: 10 * time.Millisecond, ShutdownTimeout: time.Second}
			if forced {
				cfg.ShutdownTimeout = 25 * time.Millisecond
			}
			runner := NewRunner(store, registry, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			go func() { runner.Run(ctx); close(done) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("handler never started")
			}
			stop()
			if !forced {
				select {
				case <-renewed:
				case <-time.After(time.Second):
					t.Fatal("worker stopped renewing during drain")
				}
				close(release)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("worker did not shut down")
			}
			if forced && len(store.outcome) != 0 || !forced && len(store.outcome) == 0 {
				t.Fatalf("unexpected completion count %d", len(store.outcome))
			}
		})
	}
}

func TestHeartbeatRenewalErrors(t *testing.T) {
	for _, recoverLease := range []bool{true, false} {
		name := "outage past deadline"
		if recoverLease {
			name = "transient error"
		}
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(t.Context(), 3*time.Second)
			defer stop()
			jobCtx, cancelJob := context.WithCancel(ctx)
			defer cancelJob()
			calls := 0
			store := &workerTestStore{renew: func() (bool, bool, error) {
				calls++
				if recoverLease && calls == 2 {
					if jobCtx.Err() != nil {
						t.Fatal("job cancelled before lease recovered")
					}
					stop()
					return false, true, nil
				}
				return false, false, errors.New("database unavailable")
			}}
			runner := NewRunner(store, &Registry{}, config.WorkerConfig{
				ID: " node-1 ", LeaseDuration: 200 * time.Millisecond, HeartbeatInterval: 10 * time.Millisecond,
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if runner.id != "node-1" {
				t.Fatalf("worker ID = %q, want node-1", runner.id)
			}
			started := time.Now()
			got := runner.heartbeat(ctx, cancelJob, uuid.New(), uuid.New())
			want := heartbeatLost
			if recoverLease {
				want = heartbeatStopped
			} else if time.Since(started) < runner.cfg.LeaseDuration || jobCtx.Err() == nil {
				t.Fatal("lease loss must cancel the job at or after the lease deadline")
			}
			if got != want || calls < 2 {
				t.Fatalf("heartbeat outcome = %v, want %v, renewal calls = %d", got, want, calls)
			}
		})
	}
}

func TestMockFailBlankMessage(t *testing.T) {
	for _, payload := range []string{`{"error_message":""}`, `{"error_message":"  \t","permanent":true}`} {
		_, err := mockFail(t.Context(), domain.Job{Payload: json.RawMessage(payload)})
		if err == nil || err.Error() != "simulated failure" {
			t.Fatalf("mockFail(%s) = %v, want simulated failure", payload, err)
		}
	}
}
