package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"projectlens/internal/gitscan"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"projectlens/internal/config"
	"projectlens/internal/domain"
)

type JobStore interface {
	CreateSync(context.Context, domain.Job, domain.SyncTarget) (domain.Job, bool, error)
	Create(context.Context, domain.Job) (domain.Job, error)
	GetByID(context.Context, uuid.UUID) (domain.Job, error)
	GetByIdempotencyKey(context.Context, string) (domain.Job, error)
	List(context.Context, domain.ListJobsFilter) (domain.JobListResult, error)
	RequestCancel(context.Context, uuid.UUID) (domain.Job, error)
}

type JobTypeValidator interface{ Has(string) bool }

type JobService struct {
	store    JobStore
	types    JobTypeValidator
	defaults config.WorkerConfig
}

func NewJobService(store JobStore, types JobTypeValidator, cfg config.WorkerConfig) *JobService {
	return &JobService{store: store, types: types, defaults: cfg}
}

var jobTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,119}$`)

func (s *JobService) Enqueue(ctx context.Context, input domain.CreateJobInput) (domain.Job, bool, error) {
	var empty domain.Job
	if !jobTypePattern.MatchString(input.Type) || !s.types.Has(input.Type) {
		return empty, false, fmt.Errorf("%w: type must be a registered job type", domain.ErrInvalidInput)
	}
	payload := bytes.TrimSpace(input.Payload)
	if len(input.Payload) == 0 {
		payload = []byte("{}")
	}
	if !json.Valid(payload) || payload[0] != '{' || !utf8.Valid(payload) {
		return empty, false, fmt.Errorf("%w: payload must be a valid JSON object", domain.ErrInvalidInput)
	}
	if key := input.IdempotencyKey; key != nil &&
		(strings.TrimSpace(*key) == "" || utf8.RuneCountInString(*key) > 200 || !utf8.ValidString(*key) || strings.ContainsRune(*key, 0)) {
		return empty, false, fmt.Errorf("%w: idempotency_key must be nonblank and at most 200 characters", domain.ErrInvalidInput)
	}
	job := domain.Job{ID: uuid.New(), Type: input.Type, Status: domain.JobPending, Payload: payload,
		IdempotencyKey: input.IdempotencyKey, MaxAttempts: s.defaults.DefaultMaxAttempts,
		TimeoutSeconds: int(s.defaults.DefaultTimeout / time.Second)}
	if input.MaxAttempts != nil {
		job.MaxAttempts = *input.MaxAttempts
	}
	if input.TimeoutSeconds != nil {
		job.TimeoutSeconds = *input.TimeoutSeconds
	}
	if job.MaxAttempts < 1 || job.MaxAttempts > 100 {
		return empty, false, fmt.Errorf("%w: max_attempts must be between 1 and 100", domain.ErrInvalidInput)
	}
	if job.TimeoutSeconds < 1 || job.TimeoutSeconds > 86400 {
		return empty, false, fmt.Errorf("%w: timeout_seconds must be between 1 and 86400", domain.ErrInvalidInput)
	}
	if input.RunAt != nil {
		if input.RunAt.IsZero() || input.RunAt.UTC().Year() < 1 || input.RunAt.UTC().Year() > 9999 {
			return empty, false, fmt.Errorf("%w: run_at must be a nonzero timestamp in years 1 through 9999", domain.ErrInvalidInput)
		}
		job.RunAt = input.RunAt.UTC()
	}
	if job.Type == "repository.sync" {
		var target domain.SyncTarget
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&target) != nil || target.RepositoryID == uuid.Nil || target.ProjectID == uuid.Nil {
			return empty, false, fmt.Errorf("%w: expected project_id, repository_id and optional ref", domain.ErrInvalidInput)
		}
		ref, err := gitscan.NormalizeRef(target.Ref)
		if err != nil {
			return empty, false, err
		}
		target.Ref = ref
		job.Payload, _ = json.Marshal(target)
		return s.store.CreateSync(ctx, job, target)
	}
	created, err := s.store.Create(ctx, job)
	if !errors.Is(err, domain.ErrDuplicateJobKey) || input.IdempotencyKey == nil {
		return created, err == nil, err
	}
	existing, err := s.store.GetByIdempotencyKey(ctx, *input.IdempotencyKey)
	if err != nil {
		return empty, false, err
	}
	if existing.Type != job.Type || !equalJobPayload(existing.Payload, job.Payload) {
		return empty, false, fmt.Errorf("%w: idempotency key is already used with a different type or payload", domain.ErrConflict)
	}
	return existing, false, nil
}

func equalJobPayload(a, b json.RawMessage) bool {
	decode := func(raw json.RawMessage) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return normalizeJobJSON(value), err
	}
	left, leftErr := decode(a)
	right, rightErr := decode(b)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(left, right)
}

// Match JSONB numeric equality while preserving precision and avoiding expansion of exponents.
func normalizeJobJSON(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = normalizeJobJSON(item)
		}
	case []any:
		for i, item := range v {
			v[i] = normalizeJobJSON(item)
		}
	case json.Number:
		mantissa, exponentText, _ := strings.Cut(strings.ToLower(string(v)), "e")
		whole, fraction, _ := strings.Cut(mantissa, ".")
		digits := strings.TrimLeft(strings.TrimPrefix(whole, "-")+fraction, "0")
		if digits == "" {
			return json.Number("0")
		}
		significant := strings.TrimRight(digits, "0")
		var exponent big.Int
		exponent.SetString(exponentText, 10)
		exponent.Add(&exponent, big.NewInt(int64(len(digits)-len(significant)-len(fraction))))
		if strings.HasPrefix(whole, "-") {
			significant = "-" + significant
		}
		return json.Number(significant + "e" + exponent.String())
	}
	return value
}

func parseJobID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return id, fmt.Errorf("%w: job id must be a UUID", domain.ErrInvalidInput)
	}
	return id, nil
}

func (s *JobService) GetByID(ctx context.Context, raw string) (domain.Job, error) {
	id, err := parseJobID(raw)
	if err != nil {
		return domain.Job{}, err
	}
	return s.store.GetByID(ctx, id)
}

func (s *JobService) Cancel(ctx context.Context, raw string) (domain.Job, error) {
	id, err := parseJobID(raw)
	if err != nil {
		return domain.Job{}, err
	}
	return s.store.RequestCancel(ctx, id)
}

func (s *JobService) List(ctx context.Context, filter domain.ListJobsFilter) (domain.JobListResult, error) {
	if filter.Limit == 0 {
		filter.Limit = 20
	}
	if filter.Limit < 1 || filter.Limit > 100 || filter.Offset < 0 {
		return domain.JobListResult{}, fmt.Errorf("%w: limit must be between 1 and 100 and offset must be nonnegative", domain.ErrInvalidInput)
	}
	switch filter.Status {
	case "", domain.JobPending, domain.JobRunning, domain.JobSucceeded, domain.JobFailed, domain.JobCancelled:
	default:
		return domain.JobListResult{}, fmt.Errorf("%w: invalid job status", domain.ErrInvalidInput)
	}
	if filter.Type != "" && !jobTypePattern.MatchString(filter.Type) {
		return domain.JobListResult{}, fmt.Errorf("%w: invalid job type", domain.ErrInvalidInput)
	}
	return s.store.List(ctx, filter)
}
