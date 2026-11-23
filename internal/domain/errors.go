package domain

import "errors"

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("project not found")
	ErrConflict     = errors.New("project slug already exists")
)

var (
	ErrForbidden           = errors.New("forbidden")
	ErrRateLimited         = errors.New("upstream rate limit exceeded")
	ErrUpstreamUnavailable = errors.New("upstream service unavailable")
)

var ErrDuplicateJobKey = errors.New("duplicate job idempotency key")

var (
	ErrVersionConflict = errors.New("document version conflict")
	ErrPayloadTooLarge = errors.New("payload too large")
)
