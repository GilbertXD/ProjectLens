package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"projectlens/internal/domain"
	"projectlens/internal/gitscan"
)

type scanStoreFunc func(context.Context, uuid.UUID, uuid.UUID) (domain.Repository, error)

func (f scanStoreFunc) GetByID(ctx context.Context, projectID, repoID uuid.UUID) (domain.Repository, error) {
	return f(ctx, projectID, repoID)
}

type scanFunc func(context.Context, gitscan.SourceSpec, gitscan.ScanOptions) (*gitscan.Snapshot, error)

func (f scanFunc) Scan(ctx context.Context, spec gitscan.SourceSpec, options gitscan.ScanOptions) (*gitscan.Snapshot, error) {
	return f(ctx, spec, options)
}

func TestRepositoryScanDispatch(t *testing.T) {
	projectID, repoID := uuid.New(), uuid.New()
	localPath, emptyPath := "/canonical/repo", ""
	local := gitscan.SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: "/path/to/repo"}
	remote := gitscan.SourceSpec{SourceType: domain.RepositorySourceGitHub, Source: "https://github.com/owner/old", Ref: "main"}
	for _, tc := range []struct {
		name       string
		spec       gitscan.SourceSpec
		localPath  *string
		remoteURL  string
		wantSource string
	}{
		{name: "canonical local path", spec: local, localPath: &localPath, wantSource: localPath},
		{name: "local fallback for nil path", spec: local, wantSource: local.Source},
		{name: "local fallback for empty path", spec: local, localPath: &emptyPath, wantSource: local.Source},
		{name: "canonical GitHub URL", spec: remote, remoteURL: "https://github.com/owner/new", wantSource: "https://github.com/owner/new"},
		{name: "GitHub fallback", spec: remote, wantSource: remote.Source},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec
			want := spec
			want.Source = tc.wantSource
			store := scanStoreFunc(func(_ context.Context, project, repo uuid.UUID) (domain.Repository, error) {
				if project != projectID || repo != repoID {
					t.Fatalf("unexpected repository lookup: %s %s", project, repo)
				}
				return domain.Repository{SourceType: spec.SourceType, Source: spec.Source, LocalPath: tc.localPath, RemoteURL: tc.remoteURL}, nil
			})
			scanner := scanFunc(func(_ context.Context, got gitscan.SourceSpec, options gitscan.ScanOptions) (*gitscan.Snapshot, error) {
				if got != want || options.IncludeContent {
					t.Fatalf("unexpected scan: %+v %+v", got, options)
				}
				return &gitscan.Snapshot{SourceType: got.SourceType, Files: []gitscan.FileEntry{{Path: "a.txt"}}}, nil
			})
			registry := &Registry{}
			if err := RegisterScanHandler(registry, store, scanner); err != nil {
				t.Fatal(err)
			}
			handler, ok := registry.Lookup("repository.scan")
			if !ok {
				t.Fatal("scan handler was not registered")
			}
			payload := map[string]string{"project_id": projectID.String(), "repository_id": repoID.String()}
			if spec.Ref != "" {
				payload["ref"] = spec.Ref
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			result, err := handler(t.Context(), domain.Job{Payload: raw})
			if err != nil {
				t.Fatal(err)
			}
			var snapshot gitscan.Snapshot
			if err := json.Unmarshal(result, &snapshot); err != nil || snapshot.SourceType != spec.SourceType ||
				len(snapshot.Files) != 1 || snapshot.Files[0].Path != "a.txt" {
				t.Fatalf("invalid scan result: %s, %v", result, err)
			}
		})
	}
}

func TestRepositoryScanErrorHandling(t *testing.T) {
	valid := fmt.Sprintf(`{"project_id":%q,"repository_id":%q}`, uuid.New(), uuid.New())
	for _, tc := range []struct {
		name, payload, code string
		storeErr, scanErr   error
	}{
		{name: "missing IDs", payload: "{}", code: "invalid_payload"},
		{name: "malformed UUID", payload: `{"project_id":"wrong"}`, code: "invalid_payload"},
		{name: "trailing JSON", payload: valid + " {}", code: "invalid_payload"},
		{name: "unknown key", payload: `{"unknown":true}`, code: "invalid_payload"},
		{name: "missing repo", payload: valid, code: "not_found", storeErr: domain.ErrNotFound},
		{name: "invalid source or ref", payload: valid, code: "invalid_input", scanErr: domain.ErrInvalidInput},
		{name: "forbidden source", payload: valid, code: "forbidden", scanErr: domain.ErrForbidden},
		{name: "rate limited", payload: valid, code: "handler_error", scanErr: domain.ErrRateLimited},
		{name: "transient upstream", payload: valid, code: "handler_error", scanErr: domain.ErrUpstreamUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := scanStoreFunc(func(context.Context, uuid.UUID, uuid.UUID) (domain.Repository, error) {
				if tc.code == "invalid_payload" {
					t.Fatal("invalid payload reached the store")
				}
				return domain.Repository{}, tc.storeErr
			})
			scanner := scanFunc(func(context.Context, gitscan.SourceSpec, gitscan.ScanOptions) (*gitscan.Snapshot, error) {
				if tc.storeErr != nil {
					t.Fatal("failed lookup reached the scanner")
				}
				return nil, tc.scanErr
			})
			registry := &Registry{}
			if err := RegisterScanHandler(registry, store, scanner); err != nil {
				t.Fatal(err)
			}
			handler, _ := registry.Lookup("repository.scan")
			_, err := handler(t.Context(), domain.Job{Payload: json.RawMessage(tc.payload)})
			var permanent *PermanentError
			if err == nil || ErrorCode(err) != tc.code || errors.As(err, &permanent) != (tc.code != "handler_error") {
				t.Fatalf("unexpected scan error: %v", err)
			}
		})
	}
}
