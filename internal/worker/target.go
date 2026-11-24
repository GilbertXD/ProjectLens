package worker

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"projectlens/internal/domain"
)

func RegisterTarget[T any](r *Registry, name string, fn func(context.Context, domain.SyncTarget) (T, error)) error {
	return r.Register(name, func(ctx context.Context, j domain.Job) (json.RawMessage, error) {
		var target domain.SyncTarget
		if json.Unmarshal(j.Payload, &target) != nil || target.ProjectID == uuid.Nil || target.RepositoryID == uuid.Nil {
			return nil, Permanent("invalid_payload", domain.ErrInvalidInput)
		}
		result, err := fn(ctx, target)
		if err != nil {
			return nil, scanJobError(err)
		}
		return json.Marshal(result)
	})
}
