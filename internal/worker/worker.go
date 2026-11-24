package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/domain"
	"projectlens/internal/telemetry"
)

type Store interface {
	ClaimNext(context.Context, string, uuid.UUID, time.Duration) (domain.Job, error)
	RenewLease(context.Context, uuid.UUID, string, uuid.UUID, time.Duration) (bool, bool, error)
	CompleteSucceeded(context.Context, uuid.UUID, string, uuid.UUID, json.RawMessage) (bool, error)
	CompleteFailed(context.Context, uuid.UUID, string, uuid.UUID, string, string, bool, time.Duration) (bool, error)
	MarkCancelled(context.Context, uuid.UUID, string, uuid.UUID, string) (bool, error)
	ReclaimExpiredLeases(context.Context, int, time.Duration) (int64, error)
}

type Runner struct {
	store    Store
	registry *Registry
	cfg      config.WorkerConfig
	log      *slog.Logger
	id       string
}

func NewRunner(store Store, registry *Registry, cfg config.WorkerConfig, log *slog.Logger) *Runner {
	id := strings.TrimSpace(cfg.ID)
	if id == "" {
		id = uuid.NewString()
	}
	return &Runner{store: store, registry: registry, cfg: cfg, log: log, id: id}
}

func (r *Runner) Run(ctx context.Context) {
	active, abort := context.WithCancel(context.WithoutCancel(ctx))
	defer abort()
	var loops sync.WaitGroup
	for i := 0; i < r.cfg.Concurrency; i++ {
		loops.Go(func() { r.claim(ctx, active) })
	}
	loops.Go(func() { r.sweep(ctx) })
	done := make(chan struct{})
	go func() {
		loops.Wait()
		close(done)
	}()
	<-ctx.Done()
	timer := time.NewTimer(r.cfg.ShutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		abort()
		<-done
	}
}

func (r *Runner) claim(ctx, active context.Context) {
	for ctx.Err() == nil {
		token := uuid.New()
		job, err := r.store.ClaimNext(ctx, r.id, token, r.cfg.LeaseDuration)
		if err == nil {
			r.execute(active, job, token)
			continue
		}
		if !errors.Is(err, domain.ErrNotFound) && ctx.Err() == nil {
			r.log.Error("claim job", "worker_id", r.id, "error", err)
		}
		if !waitPoll(ctx, r.cfg.PollInterval) {
			return
		}
	}
}

func (r *Runner) sweep(ctx context.Context) {
	for ctx.Err() == nil {
		const batch = 100
		count, err := r.store.ReclaimExpiredLeases(ctx, batch, r.cfg.RetryBaseDelay)
		if err == nil && count > 0 {
			telemetry.Expiries.Add(float64(count))
		}
		if err != nil && ctx.Err() == nil {
			r.log.Error("reclaim expired jobs", "worker_id", r.id, "error", err)
		}
		if err == nil && count == batch {
			continue
		}
		if !waitPoll(ctx, r.cfg.PollInterval) {
			return
		}
	}
}

func waitPoll(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type heartbeatOutcome int

const (
	heartbeatStopped heartbeatOutcome = iota
	heartbeatCancelled
	heartbeatLost
)

func (r *Runner) heartbeat(ctx context.Context, cancelJob context.CancelFunc, id, token uuid.UUID) heartbeatOutcome {
	ticker := time.NewTicker(r.cfg.HeartbeatInterval)
	defer ticker.Stop()
	leaseDeadline := time.Now().Add(r.cfg.LeaseDuration)
	expiry := time.NewTimer(time.Until(leaseDeadline))
	defer expiry.Stop()
	for {
		select {
		case <-ctx.Done():
			return heartbeatStopped
		case <-expiry.C:
		case <-ticker.C:
			started := time.Now()
			if started.Before(leaseDeadline) {
				renewCtx, cancel := context.WithDeadline(ctx, leaseDeadline)
				requested, renewed, err := r.store.RenewLease(renewCtx, id, r.id, token, r.cfg.LeaseDuration)
				cancel()
				switch {
				case err == nil && renewed && requested:
					cancelJob()
					return heartbeatCancelled
				case err == nil && renewed:
					// The database may have extended the lease before the response arrived.
					leaseDeadline = started.Add(r.cfg.LeaseDuration)
					expiry.Reset(time.Until(leaseDeadline))
					continue
				case err != nil:
					if ctx.Err() != nil {
						return heartbeatStopped
					}
					r.log.Warn("job lease renewal failed", "job_id", id, "worker_id", r.id, "error", err)
					if time.Now().Before(leaseDeadline) {
						continue
					}
				}
			}
		}
		r.log.Warn("job lease lost", "job_id", id, "worker_id", r.id)
		cancelJob()
		return heartbeatLost
	}
}

func (r *Runner) execute(active context.Context, job domain.Job, token uuid.UUID) {
	jobCtx, cancelJob := context.WithTimeout(active, time.Duration(job.TimeoutSeconds)*time.Second)
	defer cancelJob()
	heartbeatCtx, stopHeartbeat := context.WithCancel(active)
	outcome := make(chan heartbeatOutcome, 1)
	go func() { outcome <- r.heartbeat(heartbeatCtx, cancelJob, job.ID, token) }()
	jobCtx, span := telemetry.Span(telemetry.Restore(jobCtx, job.TraceParent), "worker.execute")
	defer span.End()
	start := time.Now()
	if job.Attempt > 1 {
		telemetry.Retries.Inc()
	}
	result, handlerErr := r.invoke(jobCtx, job)
	outcomeLabel := "success"
	if handlerErr != nil {
		outcomeLabel = "error"
	}
	if jobCtx.Err() != nil {
		outcomeLabel = "interrupted"
	}
	telemetry.Jobs.WithLabelValues(telemetry.JobKind(job.Type), outcomeLabel).Observe(time.Since(start).Seconds())
	traceID := telemetry.TraceID(jobCtx)
	r.log.InfoContext(jobCtx, "job attempt finished", "job_id", job.ID, "job_type", job.Type, "worker_id", r.id,
		"trace_id", traceID, "outcome", outcomeLabel, "attempt", job.Attempt)
	jobErr := jobCtx.Err()
	stopHeartbeat()
	heartbeat := <-outcome
	if heartbeat == heartbeatLost || active.Err() != nil {
		return
	}

	completion, cancel := context.WithTimeout(active, r.cfg.LeaseDuration)
	defer cancel()
	var updated bool
	var err error
	switch {
	case heartbeat == heartbeatCancelled:
		updated, err = r.store.MarkCancelled(completion, job.ID, r.id, token, "cancelled by request")
	case errors.Is(jobErr, context.DeadlineExceeded) || errors.Is(handlerErr, context.DeadlineExceeded):
		updated, err = r.store.CompleteFailed(completion, job.ID, r.id, token, "timeout", context.DeadlineExceeded.Error(), true, r.retryDelay(job.Attempt))
	case handlerErr != nil:
		var permanent *PermanentError
		delay := r.retryDelay(job.Attempt)
		var rate interface{ RetryAfter() time.Duration }
		if errors.As(handlerErr, &rate) {
			delay = max(delay, rate.RetryAfter())
		}
		updated, err = r.store.CompleteFailed(completion, job.ID, r.id, token, ErrorCode(handlerErr), handlerErr.Error(),
			!errors.As(handlerErr, &permanent), delay)
	default:
		updated, err = r.store.CompleteSucceeded(completion, job.ID, r.id, token, result)
		if err == nil && !updated {
			updated, err = r.store.MarkCancelled(completion, job.ID, r.id, token, "cancelled by request")
		}
	}
	if err != nil {
		r.log.Error("complete job", "job_id", job.ID, "job_type", job.Type, "worker_id", r.id, "trace_id", traceID, "error", err)
	} else if !updated {
		r.log.Warn("job completion rejected by lease fence", "job_id", job.ID, "job_type", job.Type, "worker_id", r.id, "trace_id", traceID)
	}
}

func (r *Runner) invoke(ctx context.Context, job domain.Job) (result json.RawMessage, err error) {
	defer func() {
		if value := recover(); value != nil {
			err = Permanent("panic", fmt.Errorf("job handler panicked: %v", value))
		}
	}()
	handler, ok := r.registry.Lookup(job.Type)
	if !ok {
		return nil, Permanent("unknown_job_type", fmt.Errorf("no handler for job type %q", job.Type))
	}
	result, err = handler(ctx, job)
	if len(result) == 0 {
		return nil, err
	}
	if err == nil && !json.Valid(result) {
		return nil, Permanent("invalid_result", errors.New("job handler returned invalid JSON"))
	}
	return result, err
}

func (r *Runner) retryDelay(attempt int) time.Duration {
	delay := r.cfg.RetryBaseDelay
	for i := 1; i < attempt; i++ {
		if delay > r.cfg.RetryMaxDelay/2 {
			return r.cfg.RetryMaxDelay
		}
		delay *= 2
	}
	return delay
}
