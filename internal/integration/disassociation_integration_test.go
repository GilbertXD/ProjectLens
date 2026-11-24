package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/repository"
	"projectlens/internal/service"
)

// disassociationFixture creates a project, one local Git repository row, and one
// repository-owned document. Rows are written directly so these tests can focus
// on the disassociation transaction instead of repository metadata discovery.
func disassociationFixture(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	cfg := database(t)
	pool := openPool(t, cfg)
	t.Cleanup(pool.Close)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	projectID, repoID, docID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx,
		"INSERT INTO projects(id,slug,name,description,status) VALUES($1,'disassociation','Disassociation','','active')",
		projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO repositories
		(id,project_id,source_type,name,source,canonical_source,default_branch,description,remote_url,local_path)
		VALUES($1,$2,'local_git','Former repository','/former','/former','main','','','/former')`, repoID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO documents(id,project_id,title,repository_id) VALUES($1,$2,'Kept note',$3)", docID, projectID, repoID); err != nil {
		t.Fatal(err)
	}
	return pool, projectID, repoID, docID
}

// TestDisassociationKeepsConcurrentDocumentProvenance holds an uncommitted
// repository-owned document while the disassociation runs. The document takes a
// FOR KEY SHARE lock on the repository row, so disassociation must serialise
// behind it and still record provenance for the document created mid-flight.
func TestDisassociationKeepsConcurrentDocumentProvenance(t *testing.T) {
	pool, projectID, repoID, docID := disassociationFixture(t)
	ctx := t.Context()

	concurrent, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = concurrent.Rollback(context.Background()) }()
	concurrentDocID := uuid.New()
	if _, err := concurrent.Exec(ctx,
		"INSERT INTO documents(id,project_id,title,repository_id) VALUES($1,$2,'Concurrent note',$3)", concurrentDocID, projectID, repoID); err != nil {
		t.Fatal(err)
	}

	deleted := make(chan error, 1)
	go func() {
		deleted <- repository.NewPostgresRepositoryStore(pool).Delete(context.Background(), projectID, repoID)
	}()

	waitForDisassociationWaiting(t, pool)
	if err := concurrent.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatalf("disassociate repository: %v", err)
	}

	for _, id := range []uuid.UUID{docID, concurrentDocID} {
		var repositoryID *uuid.UUID
		var version int64
		var originID, originName *string
		if err := pool.QueryRow(ctx, `SELECT repository_id, version,
			metadata->'origin'->>'repository_id', metadata->'origin'->>'repository_name'
			FROM documents WHERE id = $1`, id).Scan(&repositoryID, &version, &originID, &originName); err != nil {
			t.Fatalf("read document %s: %v", id, err)
		}
		if repositoryID != nil || version != 2 || originID == nil || *originID != repoID.String() ||
			originName == nil || *originName != "Former repository" {
			t.Fatalf("document %s = scope %v, version %d, origin %v/%v", id, repositoryID, version, originID, originName)
		}
	}
}

// waitForDisassociationWaiting blocks until the disassociation transaction is
// waiting on a lock held by the concurrent document transaction. This keeps the
// test deterministic instead of racing on a sleep.
func waitForDisassociationWaiting(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_locks l
			JOIN pg_class c ON c.oid = l.relation
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE NOT l.granted AND n.nspname = current_schema()
				AND c.relname IN ('repositories', 'documents')`).Scan(&waiting); err != nil {
			t.Fatalf("inspect locks: %v", err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("disassociation never waited on the repository row lock")
}

// TestDisassociationStopsSyncRepublish proves a worker cannot publish repository
// data after the association is gone.
func TestDisassociationStopsSyncRepublish(t *testing.T) {
	pool, projectID, repoID, _ := disassociationFixture(t)
	ctx := t.Context()
	store := repository.NewPostgresRepositoryStore(pool)
	if err := store.Delete(ctx, projectID, repoID); err != nil {
		t.Fatalf("disassociate repository: %v", err)
	}
	snapshots, err := repository.NewPostgresSnapshotStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshots.Close)
	sync := service.NewSyncService(store, snapshots, db.NewPgxTxManager(pool), nil, config.SyncConfig{}, nil)
	if _, err := sync.SyncRepository(ctx, projectID, repoID, "main", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("sync of disassociated repository = %v, want not found", err)
	}
	var published int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM snapshots WHERE repository_id = $1", repoID).Scan(&published); err != nil || published != 0 {
		t.Fatalf("snapshots after disassociation = %d, %v", published, err)
	}
}
