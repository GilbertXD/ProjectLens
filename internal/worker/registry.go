package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"projectlens/internal/domain"
)

type HandlerFunc func(context.Context, domain.Job) (json.RawMessage, error)

type PermanentError struct {
	Code string
	Err  error
}

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

func Permanent(code string, err error) error {
	return &PermanentError{Code: code, Err: err}
}

func ErrorCode(err error) string {
	var permanent *PermanentError
	if errors.As(err, &permanent) && permanent.Code != "" {
		return permanent.Code
	}
	var rate interface{ RetryAfter() time.Duration }
	if errors.As(err, &rate) {
		return "github_rate_limit"
	}
	return "handler_error"
}

type Registry struct {
	mu       sync.RWMutex
	handlers map[string]HandlerFunc
}

func (r *Registry) Register(name string, handler HandlerFunc) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == "" || handler == nil {
		return errors.New("job type and handler are required")
	}
	if _, exists := r.handlers[name]; exists {
		return fmt.Errorf("job type %q already registered", name)
	}
	if r.handlers == nil {
		r.handlers = make(map[string]HandlerFunc)
	}
	r.handlers[name] = handler
	return nil
}

func (r *Registry) Lookup(name string) (HandlerFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, ok := r.handlers[name]
	return handler, ok
}

func (r *Registry) Has(name string) bool {
	_, ok := r.Lookup(name)
	return ok
}

func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]string, 0, len(r.handlers))
	for name := range r.handlers {
		types = append(types, name)
	}
	sort.Strings(types)
	return types
}
