package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/google/uuid"

	"projectlens/internal/domain"
)

type repositorySyncer interface {
	SyncRepository(context.Context, uuid.UUID, uuid.UUID, string, *uuid.UUID) (domain.SyncResult, error)
}

func RegisterSyncHandler(registry *Registry, syncer repositorySyncer) error {
	return registry.Register("repository.sync", func(ctx context.Context, job domain.Job) (json.RawMessage, error) {
		var payload *struct {
			ProjectID    uuid.UUID `json:"project_id"`
			RepositoryID uuid.UUID `json:"repository_id"`
			Ref          string    `json:"ref"`
		}
		decoder := json.NewDecoder(bytes.NewReader(job.Payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			return nil, Permanent("invalid_payload", err)
		}
		if payload == nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
			return nil, Permanent("invalid_payload", errors.New("expected a single sync payload object"))
		}
		if payload.ProjectID == uuid.Nil || payload.RepositoryID == uuid.Nil {
			return nil, Permanent("invalid_payload", errors.New("project_id and repository_id are required"))
		}
		result, err := syncer.SyncRepository(ctx, payload.ProjectID, payload.RepositoryID, payload.Ref, &job.ID)
		if err != nil {
			return nil, scanJobError(err)
		}
		return json.Marshal(result)
	})
}
