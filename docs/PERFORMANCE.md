# Backend measurements and regression checks

Measured on 2026-11-24 on an Apple M4, macOS arm64, Go 1.27.2, PostgreSQL 16.16 and Redis 7.4.12. Docker Desktop had 10 CPUs and 8,319,504,384 bytes of memory available to its VM. PostgreSQL and Redis ran in separate loopback-published containers. Each integration test creates a fresh PostgreSQL schema. This is a local service/store benchmark with real Git and PostgreSQL.

| Operation | Samples | P50 ms | P95 ms | Operations/s | Go allocated bytes/op |
| --- | ---: | ---: | ---: | ---: | ---: |
| Full Git scan with content, 1,000 files | 20 | 92.285 | 96.387 | 10.79 | 5,740,044 |
| Publish incremental snapshot, one changed blob | 20 | 157.017 | 174.133 | 6.30 | 5,118,961 |
| Commit page, 20 entries | 100 | 0.558 | 0.907 | 1,479.01 | 16,395 |
| PostgreSQL search, 1,000 matches, page of 20 | 100 | 12.229 | 12.939 | 80.47 | 130,742 |
| Drain 24 queued file syncs, 2 workers | 24 | 546.524 | 1,043.378 | 22.98 | — |

The scan fixture has 1,000 distinct text files of roughly 1 KiB each. The incremental run changes one file per commit and asserts exactly one blob read. The history query uses the resulting 21 commits. Search measures the database path with a common term and a warm cache of PostgreSQL pages. Queue latency is measured from job creation to completion for 24 distinct associated repositories backed by the same empty-commit Git fixture. Queue throughput is measured from worker startup until the last completion is observed. Other throughputs use the sum of operation durations, excluding fixture setup. Allocation figures include preparation between samples. The timed `go test` invocation took 13.02 seconds wall time, 6.68 seconds user CPU, 4.63 seconds system CPU and reported a maximum RSS of 568,606,720 bytes. Those process figures include Go test/build tooling and exclude the database container.

### Query plans and implementation decisions

| Actual PostgreSQL query | Planning ms | Execution ms | Shared buffer hits | Relevant plan |
| --- | ---: | ---: | ---: | --- |
| Unified search | 0.723 | 10.919 | 449 | Search-vector and snapshot bitmap indexes, followed by ranking and top-N selection |
| Commit history page | 0.046 | 0.147 | 5 | Indexed ref lookup and repository-scoped commit join |
| Worker claim candidate | 0.054 | 0.013 | 3 | Pending-job index with row lock and limit 1 |

The history importer skips metadata and diff extraction for SHAs already stored. The Git reader also omits a redundant object-type subprocess after pinning the commit. It verifies the returned object ID and commit headers. A trial materializing the search query CTE raised shared buffer hits from 451 to 2,355 and its sampled execution time from 12.523 to 13.873 ms. The shipped SQL retains the existing planner behavior. The latency table reports one measured run. Host load, cache residency and selectivity affect these results.

<details>
<summary>Unified search · EXPLAIN (ANALYZE, BUFFERS)</summary>

```text
Sort  (cost=283.00..283.01 rows=2 width=424) (actual time=10.770..10.773 rows=20 loops=1)
  Sort Key: unified_1.score DESC, unified_1.updated_at DESC, unified_1.kind, unified_1.id
  Sort Method: quicksort  Memory: 47kB
  Buffers: shared hit=449
  CTE unified
    ->  Append  (cost=199.27..236.68 rows=2 width=60) (actual time=0.935..9.315 rows=1000 loops=1)
          Buffers: shared hit=267
          ->  Nested Loop  (cost=199.27..227.16 rows=1 width=60) (actual time=0.935..9.274 rows=1000 loops=1)
                Buffers: shared hit=265
                ->  Nested Loop  (cost=0.29..16.33 rows=1 width=24) (actual time=0.009..0.010 rows=1 loops=1)
                      Join Filter: (r_1.id = s_1.repository_id)
                      Buffers: shared hit=4
                      ->  Index Scan using snapshots_one_active_per_repo_idx on snapshots s_1  (cost=0.14..8.16 rows=1 width=40) (actual time=0.004..0.005 rows=1 loops=1)
                            Buffers: shared hit=2
                      ->  Index Scan using repositories_project_type_created_idx on repositories r_1  (cost=0.14..8.16 rows=1 width=16) (actual time=0.004..0.004 rows=1 loops=1)
                            Index Cond: (project_id = '00aa05fd-087e-48c2-9474-e10c47de3855'::uuid)
                            Buffers: shared hit=2
                ->  Bitmap Heap Scan on source_files sf_1  (cost=198.99..210.79 rows=3 width=72) (actual time=0.914..1.033 rows=1000 loops=1)
                      Recheck Cond: ((snapshot_id = s_1.id) AND (search_vector @@ '''needle'''::tsquery))
                      Heap Blocks: exact=200
                      Buffers: shared hit=261
                      ->  BitmapAnd  (cost=198.99..198.99 rows=3 width=0) (actual time=0.908..0.909 rows=0 loops=1)
                            Buffers: shared hit=61
                            ->  Bitmap Index Scan on source_files_snapshot_blob_idx  (cost=0.00..12.82 rows=587 width=0) (actual time=0.026..0.026 rows=1000 loops=1)
                                  Index Cond: (snapshot_id = s_1.id)
                                  Buffers: shared hit=15
                            ->  Bitmap Index Scan on idx_source_files_search_vector  (cost=0.00..185.77 rows=587 width=0) (actual time=0.864..0.864 rows=21000 loops=1)
                                  Index Cond: (search_vector @@ '''needle'''::tsquery)
                                  Buffers: shared hit=46
          ->  Bitmap Heap Scan on documents d_1  (cost=4.16..9.51 rows=1 width=60) (actual time=0.001..0.001 rows=0 loops=1)
                Recheck Cond: (project_id = '00aa05fd-087e-48c2-9474-e10c47de3855'::uuid)
                Filter: (search_vector @@ '''needle'''::tsquery)
                Buffers: shared hit=2
                ->  Bitmap Index Scan on documents_project_created_idx  (cost=0.00..4.16 rows=2 width=0) (actual time=0.000..0.000 rows=0 loops=1)
                      Index Cond: (project_id = '00aa05fd-087e-48c2-9474-e10c47de3855'::uuid)
                      Buffers: shared hit=2
  ->  Nested Loop Left Join  (cost=0.97..46.32 rows=2 width=424) (actual time=9.686..10.756 rows=20 loops=1)
        Buffers: shared hit=449
        ->  Nested Loop Left Join  (cost=0.97..40.90 rows=2 width=460) (actual time=9.684..10.746 rows=20 loops=1)
              Buffers: shared hit=449
              ->  Nested Loop Left Join  (cost=0.97..40.35 rows=2 width=524) (actual time=9.656..10.235 rows=20 loops=1)
                    Buffers: shared hit=449
                    ->  Nested Loop Left Join  (cost=0.97..39.30 rows=2 width=492) (actual time=9.655..10.229 rows=20 loops=1)
                          Buffers: shared hit=449
                          ->  Nested Loop Left Join  (cost=0.97..38.76 rows=2 width=460) (actual time=9.624..9.668 rows=20 loops=1)
                                Buffers: shared hit=449
                                ->  Nested Loop  (cost=0.19..8.23 rows=1 width=16) (actual time=9.470..9.471 rows=1 loops=1)
                                      Buffers: shared hit=269
                                      ->  Index Scan using projects_pkey on projects p  (cost=0.15..8.17 rows=1 width=8) (actual time=0.009..0.010 rows=1 loops=1)
                                            Index Cond: (id = '00aa05fd-087e-48c2-9474-e10c47de3855'::uuid)
                                            Buffers: shared hit=2
                                      ->  Aggregate  (cost=0.04..0.06 rows=1 width=8) (actual time=9.460..9.460 rows=1 loops=1)
                                            Buffers: shared hit=267
                                            ->  CTE Scan on unified  (cost=0.00..0.04 rows=2 width=0) (actual time=0.936..9.430 rows=1000 loops=1)
                                                  Buffers: shared hit=267
                                ->  Nested Loop Left Join  (cost=0.78..30.51 rows=2 width=444) (actual time=0.153..0.195 rows=20 loops=1)
                                      Join Filter: (unified_1.kind = 'document'::text)
                                      Buffers: shared hit=180
                                      ->  Nested Loop Left Join  (cost=0.63..18.15 rows=2 width=372) (actual time=0.145..0.181 rows=20 loops=1)
                                            Buffers: shared hit=140
                                            ->  Nested Loop Left Join  (cost=0.48..17.02 rows=2 width=212) (actual time=0.142..0.170 rows=20 loops=1)
                                                  Buffers: shared hit=100
                                                  ->  Nested Loop Left Join  (cost=0.34..16.70 rows=2 width=156) (actual time=0.138..0.157 rows=20 loops=1)
                                                        Join Filter: (unified_1.kind = 'source_file'::text)
                                                        Buffers: shared hit=60
                                                        ->  Limit  (cost=0.05..0.06 rows=2 width=60) (actual time=0.134..0.136 rows=20 loops=1)
                                                              ->  Sort  (cost=0.05..0.06 rows=2 width=60) (actual time=0.134..0.135 rows=20 loops=1)
                                                                    Sort Key: unified_1.score DESC, unified_1.updated_at DESC, unified_1.kind, unified_1.id
                                                                    Sort Method: top-N heapsort  Memory: 27kB
                                                                    ->  CTE Scan on unified unified_1  (cost=0.00..0.04 rows=2 width=60) (actual time=0.000..0.032 rows=1000 loops=1)
                                                        ->  Index Scan using source_files_pkey on source_files sf  (cost=0.29..8.31 rows=1 width=96) (actual time=0.001..0.001 rows=1 loops=20)
                                                              Index Cond: (id = unified_1.id)
                                                              Buffers: shared hit=60
                                                  ->  Index Scan using snapshots_pkey on snapshots s  (cost=0.14..0.16 rows=1 width=72) (actual time=0.000..0.000 rows=1 loops=20)
                                                        Index Cond: (id = sf.snapshot_id)
                                                        Buffers: shared hit=40
                                            ->  Index Scan using repositories_pkey on repositories r  (cost=0.14..0.56 rows=1 width=176) (actual time=0.000..0.000 rows=1 loops=20)
                                                  Index Cond: (id = s.repository_id)
                                                  Buffers: shared hit=40
                                      ->  Index Scan using documents_pkey on documents d  (cost=0.15..6.17 rows=1 width=88) (actual time=0.000..0.000 rows=0 loops=20)
                                            Index Cond: (id = unified_1.id)
                                            Buffers: shared hit=40
                          ->  Result  (cost=0.00..0.26 rows=1 width=32) (actual time=0.026..0.028 rows=1 loops=20)
                    ->  Result  (cost=0.00..0.52 rows=1 width=32) (actual time=0.000..0.000 rows=1 loops=20)
              ->  Result  (cost=0.00..0.27 rows=1 width=32) (actual time=0.025..0.025 rows=1 loops=20)
        ->  Result  (cost=0.00..0.02 rows=1 width=32) (actual time=0.000..0.000 rows=1 loops=20)
        SubPlan 2
          ->  Function Scan on regexp_matches part  (cost=0.00..2.66 rows=3 width=12) (never executed)
                Filter: (strpos(ts_headline('simple'::regconfig, tokens[1], '''needle'''::tsquery, 'StartSel=QQ, StopSel=ZZ'::text), 'QQ'::text) > 0)
Planning:
  Buffers: shared hit=8
Planning Time: 0.723 ms
Execution Time: 10.919 ms
```

</details>

<details>
<summary>Commit page · EXPLAIN (ANALYZE, BUFFERS)</summary>

```text
Index Scan using repository_history_refs_pkey on repository_history_refs h  (cost=0.15..19.73 rows=1 width=32) (actual time=0.109..0.114 rows=1 loops=1)
  Index Cond: ((repository_id = '6cc2f7f3-050a-4ce9-ab64-c05897ea354e'::uuid) AND (ref = 'main'::text))
  Buffers: shared hit=5
  SubPlan 1
    ->  Aggregate  (cost=11.55..11.56 rows=1 width=32) (actual time=0.073..0.073 rows=1 loops=1)
          Buffers: shared hit=3
          ->  Sort  (cost=11.54..11.54 rows=1 width=40) (actual time=0.021..0.022 rows=20 loops=1)
                Sort Key: s.pos
                Sort Method: quicksort  Memory: 34kB
                Buffers: shared hit=3
                ->  Hash Join  (cost=4.40..11.53 rows=1 width=40) (actual time=0.012..0.017 rows=20 loops=1)
                      Hash Cond: (c.sha = s.sha)
                      Buffers: shared hit=3
                      ->  Bitmap Heap Scan on repository_commits c  (cost=4.17..11.28 rows=3 width=64) (actual time=0.002..0.004 rows=21 loops=1)
                            Recheck Cond: (repository_id = h.repository_id)
                            Heap Blocks: exact=2
                            Buffers: shared hit=3
                            ->  Bitmap Index Scan on repository_commits_pkey  (cost=0.00..4.17 rows=3 width=0) (actual time=0.001..0.001 rows=21 loops=1)
                                  Index Cond: (repository_id = h.repository_id)
                                  Buffers: shared hit=1
                      ->  Hash  (cost=0.10..0.10 rows=10 width=40) (actual time=0.007..0.007 rows=20 loops=1)
                            Buckets: 1024  Batches: 1  Memory Usage: 10kB
                            ->  Function Scan on unnest s  (cost=0.00..0.10 rows=10 width=40) (actual time=0.003..0.003 rows=20 loops=1)
Planning Time: 0.046 ms
Execution Time: 0.147 ms
```

</details>

<details>
<summary>Pending job candidate · EXPLAIN (ANALYZE, BUFFERS)</summary>

```text
Limit  (cost=0.15..8.18 rows=1 width=38) (actual time=0.006..0.006 rows=1 loops=1)
  Buffers: shared hit=3
  ->  LockRows  (cost=0.15..8.18 rows=1 width=38) (actual time=0.006..0.006 rows=1 loops=1)
        Buffers: shared hit=3
        ->  Index Scan using jobs_claim_pending_idx on jobs  (cost=0.15..8.17 rows=1 width=38) (actual time=0.005..0.005 rows=1 loops=1)
              Index Cond: (run_at <= now())
              Filter: (status = 'pending'::text)
              Buffers: shared hit=2
Planning:
  Buffers: shared hit=5
Planning Time: 0.054 ms
Execution Time: 0.013 ms
```

</details>

### Reproduce

Point `TEST_DATABASE_URL` at a disposable PostgreSQL database with schema-creation privileges and `TEST_REDIS_URL` at a disposable Redis instance. On macOS the following command also collects resource accounting:

```sh
PROJECTLENS_LOAD_TEST=1 /usr/bin/time -l go test -v -count=1 \
  -run '^TestBackendLoad$' ./internal/integration
```

The test prints the complete `EXPLAIN (ANALYZE, BUFFERS)` plans for the actual server-prepared search and history statements and the job claim candidate. It creates its own Git fixtures, removes its test schema on exit and runs independently of live GitHub.

### Regression commands

```sh
gofmt -l cmd internal api migrations
git diff --check c504eeb
go vet ./...
go build ./...
docker compose config -q
go test -race -count=1 ./...
bash scripts/e2e_docker.sh
```

The integration run used real PostgreSQL and Redis, a local Git repository, and local HTTP and GitHub fixtures. Set `TEST_SCAN_LOCAL_REPO`, `TEST_SCAN_REMOTE_REPO` and `TEST_ACTIVITY_REMOTE_REPO` to include live source smoke tests. For this run the local checkout was ProjectLens and the remote was `https://github.com/GilbertXD/ProjectLens`. Its activity REST smoke returned zero records. Nonempty PRs, comments, ETags, deletions and throttling were exercised by deterministic fixtures.

The migration test builds from an empty schema, verifies versions 11 through 7 roll back to V1, inserts a V1 project, upgrades again and checks that its data survives. It also runs the original V1 down/up assertions. Docker E2E exercises local and remote snapshots, commit history, Compare, activity, sync policies, search/cache failure, job recovery and rejection of persisted mock jobs by a standalone production worker.

The Docker builder initially failed to resolve the public Go module proxy. The successful build used `GOPROXY=http://host.docker.internal:<port>` pointing to a temporary read-only mirror of the project's checksum-verified public modules on the host. The normal command works with a reachable configured Go proxy.


The deterministic race suite passed. In a separate all-live race run the remote Git scanner received a transient upstream fetch failure. The isolated live scanner retry passed, and the live snapshot and activity integrations passed. The Docker test also verified cleanup of the standalone worker and its test volumes.

### Focused acceptance checks

With the same test database, each of the following checks passed:

```sh
go test -count=1 ./internal/integration -run TestScopedSyncRequests
go test ./internal/{worker,config,handler} ./cmd/...
go test -count=1 ./internal/gitscan -run TestRemoteManifestParity
go test -race -count=1 ./internal/integration -run 'TestHistoryReachabilityAndChanges|TestHistoryPages|TestActivityReconciliation|TestDurableSchedule|TestMetricsAndTraceCorrelation'
go test -count=1 ./internal/router -run TestOpenAPIRouteParity
```

For manual acceptance, associate a local repository containing a known term, call the scoped file sync endpoint twice, follow the returned job and search for the term. Queue commit history and compare two imported SHAs. On a public GitHub repository, queue activity and open an issue or PR by number. Enable a policy and check its job and next time, then disable it and verify a manual sync still completes.
