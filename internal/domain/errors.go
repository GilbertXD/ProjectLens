package domain

import "errors"

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("project not found")
	ErrConflict     = errors.New("project slug already exists")
)
