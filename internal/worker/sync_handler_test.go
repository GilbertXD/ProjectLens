package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/google/uuid"

	"projectlens/internal/domain"
)

type syncerFunc func(context.Context, uuid.UUID, uuid.UUID, string, *uuid.UUID) (domain.SyncResult, error)

func (f syncerFunc) SyncRepository(ctx context.Context, projectID, repoID uuid.UUID, ref string, jobID *uuid.UUID) (domain.SyncResult, error) {
	return f(ctx, projectID, repoID, ref, jobID)
}

func TestRepositorySyncHandler(t *testing.T) {
	projectID, repoID, jobID := uuid.New(), uuid.New(), uuid.New()
	want := domain.SyncResult{Status: domain.SyncOutcomePublished, SnapshotID: uuid.New(), RepositoryID: repoID, Version: 1, BlobsRead: 3}
	registry := &Registry{}
	if err := RegisterSyncHandler(registry, syncerFunc(func(_ context.Context, project, repo uuid.UUID, ref string, job *uuid.UUID) (domain.SyncResult, error) {
		if project != projectID || repo != repoID || job == nil || *job != jobID || ref != "main" {
			t.Fatalf("unexpected dispatch: %s, %s, %q, %v", project, repo, ref, job)
		}
		return want, nil
	})); err != nil {
		t.Fatal(err)
	}
	h, ok := registry.Lookup("repository.sync")
	if !ok {
		t.Fatal("sync handler was not registered")
	}
	payload := fmt.Sprintf(`{"project_id":%q,"repository_id":%q,"ref":"main"}`, projectID, repoID)
	raw, err := h(t.Context(), domain.Job{ID: jobID, Payload: json.RawMessage(payload)})
	if err != nil {
		t.Fatal(err)
	}
	var got domain.SyncResult
	if err := json.Unmarshal(raw, &got); err != nil || got != want {
		t.Fatalf("decoded result: %+v, %v", got, err)
	}
}

func TestRepositorySyncHandlerErrors(t *testing.T) {
	valid := fmt.Sprintf(`{"project_id":%q,"repository_id":%q}`, uuid.New(), uuid.New())
	for _, tc := range []struct {
		name, payload, code string
		err                 error
	}{
		{"syntax", "{", "invalid_payload", nil},
		{"missing IDs", "{}", "invalid_payload", nil},
		{"null", "null", "invalid_payload", nil},
		{"array", "[]", "invalid_payload", nil},
		{"unknown field", `{"extra":true}`, "invalid_payload", nil},
		{"malformed ID", `{"project_id":"bad"}`, "invalid_payload", nil},
		{"trailing JSON", valid + " null", "invalid_payload", nil},
		{"invalid input", valid, "invalid_input", domain.ErrInvalidInput},
		{"forbidden", valid, "forbidden", domain.ErrForbidden},
		{"not found", valid, "not_found", domain.ErrNotFound},
		{"conflict", valid, "handler_error", domain.ErrConflict},
		{"rate limited", valid, "handler_error", domain.ErrRateLimited},
		{"upstream", valid, "handler_error", domain.ErrUpstreamUnavailable},
		{"IO", valid, "handler_error", io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := &Registry{}
			if err := RegisterSyncHandler(registry, syncerFunc(func(_ context.Context, _, _ uuid.UUID, ref string, _ *uuid.UUID) (domain.SyncResult, error) {
				if tc.err == nil || ref != "" {
					t.Fatalf("unexpected sync for %q, ref %q", tc.payload, ref)
				}
				return domain.SyncResult{}, fmt.Errorf("sync: %w", tc.err)
			})); err != nil {
				t.Fatal(err)
			}
			h, _ := registry.Lookup("repository.sync")
			_, err := h(t.Context(), domain.Job{Payload: json.RawMessage(tc.payload)})
			var permanent *PermanentError
			if err == nil || ErrorCode(err) != tc.code || errors.As(err, &permanent) != (tc.code != "handler_error") ||
				tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("handler error: %v", err)
			}
		})
	}
}
