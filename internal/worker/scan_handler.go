package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/google/uuid"

	"projectlens/internal/domain"
	"projectlens/internal/gitscan"
)

type scanRepositoryStore interface {
	GetByID(context.Context, uuid.UUID, uuid.UUID) (domain.Repository, error)
}

type repositoryScanner interface {
	Scan(context.Context, gitscan.SourceSpec, gitscan.ScanOptions) (*gitscan.Snapshot, error)
}

func RegisterScanHandler(registry *Registry, store scanRepositoryStore, scanner repositoryScanner) error {
	return registry.Register("repository.scan", func(ctx context.Context, job domain.Job) (json.RawMessage, error) {
		var payload struct {
			ProjectID    uuid.UUID `json:"project_id"`
			RepositoryID uuid.UUID `json:"repository_id"`
			Ref          string    `json:"ref"`
		}
		decoder := json.NewDecoder(bytes.NewReader(job.Payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			return nil, Permanent("invalid_payload", err)
		}
		if decoder.Decode(new(json.RawMessage)) != io.EOF {
			return nil, Permanent("invalid_payload", errors.New("expected a single scan payload"))
		}
		if payload.ProjectID == uuid.Nil || payload.RepositoryID == uuid.Nil {
			return nil, Permanent("invalid_payload", errors.New("project_id and repository_id are required"))
		}
		repo, err := store.GetByID(ctx, payload.ProjectID, payload.RepositoryID)
		if err != nil {
			return nil, scanJobError(err)
		}
		source := repo.Source
		switch repo.SourceType {
		case domain.RepositorySourceLocalGit:
			if repo.LocalPath != nil && *repo.LocalPath != "" {
				source = *repo.LocalPath
			}
		case domain.RepositorySourceGitHub:
			if repo.RemoteURL != "" {
				source = repo.RemoteURL
			}
		}
		snapshot, err := scanner.Scan(ctx, gitscan.SourceSpec{
			SourceType: repo.SourceType, Source: source, Ref: payload.Ref,
		}, gitscan.ScanOptions{})
		if err != nil {
			return nil, scanJobError(err)
		}
		return json.Marshal(snapshot)
	})
}

func scanJobError(err error) error {
	switch {
	case errors.Is(err, domain.ErrInvalidInput):
		return Permanent("invalid_input", err)
	case errors.Is(err, domain.ErrForbidden):
		return Permanent("forbidden", err)
	case errors.Is(err, domain.ErrNotFound):
		return Permanent("not_found", err)
	default:
		return err
	}
}
