package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

func RegisterConfiguredMocks(r *Registry, cfg config.Config) error {
	if cfg.Worker.EnableMockJobs && (cfg.App.Env == "development" || cfg.App.Env == "test") {
		return RegisterMockHandlers(r)
	}
	return nil
}

func RegisterMockHandlers(r *Registry) error {
	for name, handler := range map[string]HandlerFunc{
		"mock.echo": mockEcho, "mock.sleep": mockSleep, "mock.fail": mockFail, "mock.flaky": mockFlaky,
	} {
		if err := r.Register(name, handler); err != nil {
			return err
		}
	}
	return nil
}

func mockDelay(ctx context.Context, milliseconds int64) error {
	if milliseconds < 0 || milliseconds > math.MaxInt64/int64(time.Millisecond) {
		return Permanent("invalid_payload", errors.New("delay must be a nonnegative duration in milliseconds"))
	}
	if milliseconds == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(time.Duration(milliseconds) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func mockEcho(ctx context.Context, job domain.Job) (json.RawMessage, error) {
	var payload struct {
		Delay int64 `json:"delay_ms"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return nil, Permanent("invalid_payload", err)
	}
	if err := mockDelay(ctx, payload.Delay); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"echo": job.Payload, "attempt": job.Attempt})
}

func mockSleep(ctx context.Context, job domain.Job) (json.RawMessage, error) {
	payload := struct {
		Duration int64 `json:"duration_ms"`
	}{Duration: 100}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return nil, Permanent("invalid_payload", err)
	}
	if err := mockDelay(ctx, payload.Duration); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"slept_ms": payload.Duration, "attempt": job.Attempt})
}

func mockFail(ctx context.Context, job domain.Job) (json.RawMessage, error) {
	payload := struct {
		Delay     int64  `json:"delay_ms"`
		Message   string `json:"error_message"`
		Permanent bool   `json:"permanent"`
	}{Message: "simulated failure"}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return nil, Permanent("invalid_payload", err)
	}
	if err := mockDelay(ctx, payload.Delay); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.Message) == "" {
		payload.Message = "simulated failure"
	}
	err := errors.New(payload.Message)
	if payload.Permanent {
		err = Permanent("simulated_permanent_failure", err)
	}
	return nil, err
}

func mockFlaky(_ context.Context, job domain.Job) (json.RawMessage, error) {
	payload := struct {
		SucceedOn int `json:"succeed_on_attempt"`
	}{SucceedOn: 2}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return nil, Permanent("invalid_payload", err)
	}
	if payload.SucceedOn < 1 {
		return nil, Permanent("invalid_payload", errors.New("succeed_on_attempt must be positive"))
	}
	if job.Attempt < payload.SucceedOn {
		return nil, fmt.Errorf("simulated failure on attempt %d", job.Attempt)
	}
	return json.Marshal(map[string]any{"attempts": job.Attempt, "recovered": true})
}
