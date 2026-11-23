package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

type jobTestStore struct {
	JobStore
	saved domain.Job
}

func (s *jobTestStore) Create(_ context.Context, job domain.Job) (domain.Job, error) {
	if s.saved.IdempotencyKey != nil {
		return domain.Job{}, domain.ErrDuplicateJobKey
	}
	s.saved = job
	return job, nil
}

func (s *jobTestStore) GetByIdempotencyKey(context.Context, string) (domain.Job, error) {
	return s.saved, nil
}

func (s *jobTestStore) List(_ context.Context, f domain.ListJobsFilter) (domain.JobListResult, error) {
	return domain.JobListResult{Limit: f.Limit, Offset: f.Offset}, nil
}

type testJobTypes struct{}

func (testJobTypes) Has(name string) bool { return name == "mock.echo" }

func TestEnqueueIdempotency(t *testing.T) {
	svc := NewJobService(&jobTestStore{}, testJobTypes{}, config.WorkerConfig{DefaultTimeout: time.Minute, DefaultMaxAttempts: 4})
	key := "same-key"
	input := domain.CreateJobInput{Type: "mock.echo", Payload: []byte(`{"n":9007199254740993,"a":[0.01]}`), IdempotencyKey: &key}
	first, created, err := svc.Enqueue(t.Context(), input)
	if err != nil || !created || first.Status != domain.JobPending || first.MaxAttempts != 4 || first.TimeoutSeconds != 60 {
		t.Fatalf("created = %t, job = %+v, err = %v", created, first, err)
	}
	input.Payload = []byte(`{"a":[1e-2],"n":9007199254740993.0}`)
	second, created, err := svc.Enqueue(t.Context(), input)
	if err != nil || created || first.ID != second.ID {
		t.Fatalf("created = %t, job = %+v, err = %v", created, second, err)
	}
	input.Payload = []byte(`{"n":9007199254740992,"a":[0.01]}`)
	if _, _, err := svc.Enqueue(t.Context(), input); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v", err)
	}
}

func TestJobServiceValidation(t *testing.T) {
	svc := NewJobService(&jobTestStore{}, testJobTypes{}, config.WorkerConfig{DefaultTimeout: time.Second, DefaultMaxAttempts: 1})
	zero, invalidTimeout, blank, zeroTime := 0, 86401, " ", time.Time{}
	for _, input := range []domain.CreateJobInput{
		{Type: "unknown"}, {Type: "mock.echo", Payload: []byte(`null`)}, {Type: "mock.echo", Payload: []byte(`[]`)},
		{Type: "mock.echo", MaxAttempts: &zero}, {Type: "mock.echo", TimeoutSeconds: &invalidTimeout},
		{Type: "mock.echo", IdempotencyKey: &blank}, {Type: "mock.echo", RunAt: &zeroTime},
	} {
		if _, _, err := svc.Enqueue(t.Context(), input); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("input %+v, err = %v", input, err)
		}
	}
	job, created, err := svc.Enqueue(t.Context(), domain.CreateJobInput{Type: "mock.echo"})
	if err != nil || !created || string(job.Payload) != "{}" {
		t.Fatalf("job = %+v, created = %t, err = %v", job, created, err)
	}
	list, err := svc.List(t.Context(), domain.ListJobsFilter{})
	if err != nil || list.Limit != 20 {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
	for _, filter := range []domain.ListJobsFilter{{Limit: 101}, {Offset: -1}, {Status: "bad"}, {Type: "INVALID"}} {
		if _, err := svc.List(t.Context(), filter); !errors.Is(err, domain.ErrInvalidInput) {
			t.Fatalf("filter = %+v, err = %v", filter, err)
		}
	}
	if _, err := svc.Cancel(t.Context(), "bad"); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("err = %v", err)
	}
}
