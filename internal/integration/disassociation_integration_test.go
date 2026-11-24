package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"projectlens/internal/config"
	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/repository"
	"projectlens/internal/service"
	"projectlens/internal/worker"
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
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

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
		deleted <- repository.NewPostgresRepositoryStore(pool).Delete(ctx, projectID, repoID)
	}()

	waitForDisassociationWaiting(t, ctx, pool, concurrent.Conn().PgConn().PID())
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
func waitForDisassociationWaiting(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blockerPID uint32) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE $1::integer = ANY(pg_blocking_pids(pid)))`, int32(blockerPID)).Scan(&waiting); err != nil {
			t.Fatalf("inspect locks: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("disassociation never waited on the repository row lock")
		case <-ticker.C:
		}
	}
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

type pausedSnapshotPublisher struct {
	*repository.PostgresSnapshotStore
	ready     chan struct{}
	resume    chan struct{}
	completed chan error
}

func (p *pausedSnapshotPublisher) PublishSnapshot(ctx context.Context, tx db.TxManager, req repository.PublishSnapshotRequest) (domain.Snapshot, error) {
	close(p.ready)
	select {
	case <-p.resume:
	case <-ctx.Done():
		return domain.Snapshot{}, ctx.Err()
	}
	snapshot, err := p.PostgresSnapshotStore.PublishSnapshot(ctx, tx, req)
	p.completed <- err
	return snapshot, err
}

func TestDisassociationCancelsRunningSyncBeforePublish(t *testing.T) {
	f := localSyncFixture(t)
	f.write(t, "source.txt", "initial contents\n")
	f.commit(t, "initial snapshot")
	f.sync(t, "main")
	f.write(t, "source.txt", "updated contents\n")
	f.commit(t, "changed snapshot")

	publisher := &pausedSnapshotPublisher{
		PostgresSnapshotStore: f.store,
		ready:                 make(chan struct{}),
		resume:                make(chan struct{}),
		completed:             make(chan error, 1),
	}
	syncer := service.NewSyncService(f.repos, publisher, f.tx, f.scanner, f.cfg.Sync, f.log)
	registry := &worker.Registry{}
	if err := worker.RegisterSyncHandler(registry, syncer); err != nil {
		t.Fatal(err)
	}
	jobs := repository.NewPostgresJobStore(f.pool)
	cfg := f.cfg.Worker
	// Let the publish attempt reach PostgreSQL before the heartbeat can cancel it.
	cfg.HeartbeatInterval = 30 * time.Second
	cfg.LeaseDuration = time.Minute
	jobService := service.NewJobService(jobs, registry, cfg)
	payload := fmt.Sprintf(`{"project_id":%q,"repository_id":%q,"ref":"main"}`, f.projectID, f.repoID)
	queued := enqueueJob(t, jobService, "repository.sync", payload, 2, 20)
	startJobWorker(t, cfg, jobs, registry)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case <-publisher.ready:
	case <-ctx.Done():
		t.Fatal("running sync did not reach snapshot publishing")
	}
	running, err := jobs.GetByID(ctx, queued.ID)
	if err != nil || running.Status != domain.JobRunning || running.CancelRequestedAt != nil {
		t.Fatalf("job before disassociation: %+v, %v", running, err)
	}
	if err := f.repos.Delete(ctx, f.projectID, f.repoID); err != nil {
		t.Fatalf("disassociate repository: %v", err)
	}
	running, err = jobs.GetByID(ctx, queued.ID)
	if err != nil || running.Status != domain.JobRunning || running.CancelRequestedAt == nil {
		t.Fatalf("job after disassociation: %+v, %v", running, err)
	}
	close(publisher.resume)

	finished := waitJob(t, jobs, queued.ID, jobStatus(domain.JobCancelled))
	if finished.Attempt != 1 || finished.FinishedAt == nil || len(finished.Result) != 0 {
		t.Fatalf("cancelled job: %+v", finished)
	}
	select {
	case err := <-publisher.completed:
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("publish after disassociation = %v, want not found", err)
		}
	default:
		t.Fatal("job finished without attempting to publish")
	}
	var snapshots, files int
	if err := f.pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM snapshots WHERE repository_id = $1),
		(SELECT count(*) FROM source_files WHERE repository_id = $1)`, f.repoID).Scan(&snapshots, &files); err != nil || snapshots != 0 || files != 0 {
		t.Fatalf("repository data after disassociation: %d snapshots, %d files, %v", snapshots, files, err)
	}
}
