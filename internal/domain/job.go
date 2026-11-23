package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

type Job struct {
	ID                uuid.UUID       `json:"id"`
	Type              string          `json:"type"`
	Status            JobStatus       `json:"status"`
	Payload           json.RawMessage `json:"payload"`
	Result            json.RawMessage `json:"result,omitempty"`
	IdempotencyKey    *string         `json:"idempotency_key,omitempty"`
	Attempt           int             `json:"attempt"`
	MaxAttempts       int             `json:"max_attempts"`
	TimeoutSeconds    int             `json:"timeout_seconds"`
	RunAt             time.Time       `json:"run_at"`
	LockedBy          *string         `json:"locked_by,omitempty"`
	LeaseToken        *uuid.UUID      `json:"-"`
	LeaseUntil        *time.Time      `json:"lease_until,omitempty"`
	CancelRequestedAt *time.Time      `json:"cancel_requested_at,omitempty"`
	LastError         *string         `json:"last_error,omitempty"`
	LastErrorCode     *string         `json:"last_error_code,omitempty"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type CreateJobInput struct {
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	IdempotencyKey *string         `json:"idempotency_key"`
	MaxAttempts    *int            `json:"max_attempts"`
	TimeoutSeconds *int            `json:"timeout_seconds"`
	RunAt          *time.Time      `json:"run_at"`
}

type ListJobsFilter struct {
	Type   string
	Status JobStatus
	Limit  int
	Offset int
}

type JobListResult struct {
	Items  []Job `json:"items"`
	Total  int64 `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}
