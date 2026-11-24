package integration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"projectlens/internal/db"
	"projectlens/internal/domain"
	"projectlens/internal/gitscan"
	"projectlens/internal/repository"
	"projectlens/internal/service"
	"projectlens/internal/worker"
)

// Opt in because this constructs and publishes a thousand-file repository repeatedly.
func TestBackendLoad(t *testing.T) {
	if os.Getenv("PROJECTLENS_LOAD_TEST") != "1" {
		t.Skip("set PROJECTLENS_LOAD_TEST=1")
	}
	f := localSyncFixture(t)
	ctx := t.Context()
	for i := range 1000 {
		f.write(t, fmt.Sprintf("src/file%d.txt", i), fmt.Sprintf("needle %d\n", i)+strings.Repeat(fmt.Sprintf("%x ", sha256.Sum256([]byte(fmt.Sprint(i)))), 16))
	}
	f.commit(t, "initial")
	spec := gitscan.SourceSpec{SourceType: domain.RepositorySourceLocalGit, Source: f.dir, Ref: "main"}
	report := func(label string, times []time.Duration, elapsed time.Duration) {
		slices.Sort(times)
		t.Logf("%s n=%d p50=%.3f ms p95=%.3f ms throughput=%.2f/s", label, len(times), float64(times[(len(times)-1)/2])/1e6, float64(times[int(math.Ceil(.95*float64(len(times))))-1])/1e6, float64(len(times))/elapsed.Seconds())
	}
	measure := func(label string, n int, prepare func(int), run func() error) {
		t.Helper()
		var times []time.Duration
		var elapsed time.Duration
		var begin, end runtime.MemStats
		runtime.ReadMemStats(&begin)
		for i := range n {
			if prepare != nil {
				prepare(i)
			}
			start := time.Now()
			if err := run(); err != nil {
				t.Fatal(err)
			}
			d := time.Since(start)
			times = append(times, d)
			elapsed += d
		}
		runtime.ReadMemStats(&end)
		report(label, times, elapsed)
		t.Logf("%s Go allocations including preparation: %.0f bytes/op", label, float64(end.TotalAlloc-begin.TotalAlloc)/float64(n))
	}
	measure("scan", 20, nil, func() error {
		_, err := f.scanner.Scan(ctx, spec, gitscan.ScanOptions{IncludeContent: true})
		return err
	})
	f.sync(t, "main")
	measure("incremental", 20, func(i int) { f.write(t, "src/file0.txt", fmt.Sprintf("needle update %d", i)); f.commit(t, "change") }, func() error {
		res, err := f.svc.SyncRepository(ctx, f.projectID, f.repoID, "main", nil)
		if err == nil && res.BlobsRead != 1 {
			t.Fatalf("blobs read %d", res.BlobsRead)
		}
		return err
	})
	history := service.HistoryService{Repositories: f.repos, Locks: f.store, Scanner: f.scanner, Tx: f.tx}
	target := domain.SyncTarget{ProjectID: f.projectID, RepositoryID: f.repoID, Ref: "main"}
	if _, err := history.Sync(ctx, target); err != nil {
		t.Fatal(err)
	}
	measure("history_page", 100, nil, func() error { _, err := history.Read(ctx, target, "list", "", "", 20, 0); return err })
	search := repository.NewPostgresSearchStore(f.pool)
	filter := domain.SearchFilter{Query: "needle", Scope: domain.SearchScopeAll, Limit: 20}
	measure("search", 100, nil, func() error { _, err := search.Search(ctx, f.projectID, filter); return err })
	// Capture the actual server-prepared SQL on the same session as the query.
	explain := func(conn db.DBTX, ctx context.Context, label, sql string, args ...any) error {
		rows, err := conn.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			t.Log("EXPLAIN", label, line)
		}
		return rows.Err()
	}
	err := f.tx.WithinTx(ctx, func(txctx context.Context) error {
		conn := db.ConnFromContext(txctx, f.pool)
		if _, err := search.Search(txctx, f.projectID, filter); err != nil {
			return err
		}
		if _, err := history.Read(txctx, target, "list", "", "", 20, 0); err != nil {
			return err
		}
		prepared := func(pattern string) (string, error) {
			var sql string
			err := conn.QueryRow(txctx, "SELECT statement FROM pg_prepared_statements WHERE statement LIKE $1 AND statement NOT LIKE '%pg_prepared_statements%' AND statement NOT LIKE 'EXPLAIN%' LIMIT 1", pattern).Scan(&sql)
			return sql, err
		}
		sql, err := prepared("%source_matches AS (%")
		if err != nil {
			return err
		}
		if err := explain(conn, txctx, "search", sql, f.projectID, filter.Query, []string{}, filter.RepositoryID, "", filter.Limit, filter.Offset,
			"StartSel=QQ, StopSel=ZZ", "StartSel=QQ, StopSel=ZZ, HighlightAll=true", "QQ",
			filter.Scope, filter.RepositoryIDs, filter.RepositoryRole, filter.IncludeProjectDocuments); err != nil {
			return err
		}
		sql, err = prepared("%jsonb_agg(c.metadata ORDER BY s.pos)%")
		if err != nil {
			return err
		}
		return explain(conn, txctx, "history", sql, f.repoID, target.Ref, 20, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Scope each job to a separate project linked to the same small local repository.
	small := t.TempDir()
	syncGit(t, small, "init", "--initial-branch=main")
	syncGit(t, small, "commit", "--allow-empty", "-m", "queue fixture")
	cfg := f.cfg
	cfg.Repository.AllowedLocalRoots = []string{small}
	cfg.Worker.Concurrency = 2
	reg := &worker.Registry{}
	syncer := service.NewSyncService(f.repos, f.store, f.tx, gitscan.NewScanner(cfg.Scanner, cfg.Repository, cfg.GitHub), cfg.Sync, f.log)
	if err := worker.RegisterSyncHandler(reg, syncer); err != nil {
		t.Fatal(err)
	}
	store := repository.NewPostgresJobStore(f.pool)
	jobs := service.NewJobService(store, reg, cfg.Worker)
	projects := service.NewProjectService(repository.NewPostgresProjectRepository(f.pool), f.tx)
	var ids []uuid.UUID
	for i := range 24 {
		project, err := projects.Create(ctx, domain.CreateProjectInput{Slug: fmt.Sprint("load-", i), Name: "Load"})
		if err != nil {
			t.Fatal(err)
		}
		repo, err := f.repos.Create(ctx, domain.Repository{ID: uuid.New(), ProjectID: project.ID, Name: "Load", CanonicalSource: small, Source: small, LocalPath: &small, SourceType: domain.RepositorySourceLocalGit})
		if err != nil {
			t.Fatal(err)
		}
		j, _, err := jobs.Enqueue(ctx, domain.CreateJobInput{Type: "repository.sync", Payload: []byte(fmt.Sprintf(`{"project_id":%q,"repository_id":%q}`, project.ID, repo.ID))})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	if err := explain(f.pool, ctx, "claim_candidate", "SELECT id FROM jobs WHERE status='pending' AND run_at<=now() ORDER BY run_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	startJobWorker(t, cfg.Worker, store, reg)
	var runtimes []time.Duration
	for _, id := range ids {
		j := waitJob(t, store, id, jobStatus(domain.JobSucceeded))
		runtimes = append(runtimes, j.FinishedAt.Sub(j.CreatedAt))
	}
	report("queued_sync_end_to_end", runtimes, time.Since(start))
}
