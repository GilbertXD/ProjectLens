# ProjectLens operations

## Deploy and configure

1. Install Docker with the Compose plugin. Check the rendered configuration with `docker compose config -q`. For a development smoke test, `docker compose --env-file /dev/null up --build -d` starts the API, embedded worker, PostgreSQL, and Redis with the packaged defaults.
2. For production, keep `APP_ENV=production`, `AUTH_ENABLED=true` and `LOG_FORMAT=json`. Generate a token with `openssl rand -hex 32` and inject it through `AUTH_BEARER_TOKENS` using a restricted environment file or secret manager. Configure `DATABASE_URL` for the production database. When initializing a new Compose PostgreSQL volume, set `POSTGRES_USER`, `POSTGRES_PASSWORD`, and `POSTGRES_DB` and use the matching credentials in `DATABASE_URL`. Rotate existing database credentials in PostgreSQL. Apply production Redis credentials through `REDIS_PASSWORD` when applicable.
3. Terminate TLS at an ingress or reverse proxy. Publish the API only on the intended interface, for example `API_PORT=127.0.0.1:8080` behind a proxy on the same host. Use a Compose override or a firewall to restrict the database and Redis port mappings. Set explicit `CORS_ALLOWED_ORIGINS` for trusted browser clients.
4. Keep PostgreSQL on persistent storage. The default stack uses the `postgres_data` volume. API and external workers share `scanner_cache` at `/var/cache/projectlens`. Keep this directory owned by the service user with mode `0700` and size its disk for the expected Git object cache. The runtime image runs as `app:app`.
5. Verify `/healthz`, `/readyz`, and an authenticated `/api/v1/ping` before routing traffic. Use `/openapi.yaml` or `/openapi.json` for the deployed route contract.

### Rotate bearer tokens

Set `AUTH_BEARER_TOKENS` to a comma-separated list containing the current and replacement tokens. Tokens must be unique and contain no whitespace or NUL. Recreate the API containers so each instance reads the updated list. Migrate clients to the replacement token and confirm they can authenticate. Remove the old token and recreate the API containers again. For a compromised token, remove it immediately and update clients to an active token.

### Run migrations explicitly

Set `DB_AUTO_MIGRATE=false` for the API and workers when migrations run independently. Take a database backup before an upgrade and run the migration binary from the same release as the application.

```sh
export DB_AUTO_MIGRATE=false
docker compose up -d postgres
docker compose --profile migrate run --rm --build migrate
docker compose up --build -d api
docker compose --profile migrate run --rm migrate -config /app/configs/config.yaml status
```

The migration profile runs `/app/migrate -config /app/configs/config.yaml up` by default. Use `status` to inspect the migration ledger. With a host binary, use `make migrate-up` or `make migrate-status` against the configured database.

### Choose a worker topology

- **Embedded**. The default `WORKER_MODE=embedded` runs the HTTP API and a worker in one process. Set `WORKER_CONCURRENCY` for the expected queue load.
- **External**. Use `WORKER_MODE=external docker compose --profile worker up --build -d` to run an API-only process and a separate worker. Use the same database, GitHub settings, scanner configuration, and local repository mount paths for both. Set `API_STOP_GRACE_PERIOD` and `WORKER_STOP_GRACE_PERIOD` to allow the HTTP server and worker to drain. The defaults are 30s and 20s.
- **Disabled**. `WORKER_MODE=disabled` can serve HTTP during maintenance. Pending jobs require a worker to make progress.

## Logging and diagnostics

With `LOG_FORMAT=json`, each HTTP access record includes `time`, `level`, `msg`, `request_id`, `method`, `path`, `route`, `status`, `latency_ms`, `bytes_out`, `bytes_in`, `client_ip`, `user_agent`, and `auth`. `bytes_in` is the declared content length and is `-1` for an unknown length. `route` is the Gin route template when a route matched. Access records use INFO below 400, WARN for 4xx, and ERROR for 5xx. Startup and shutdown records identify the process, and production emits a warning when API auth is disabled.

Correlate a client response with its access record using `X-Request-ID`. Clients may supply up to 128 visible ASCII bytes. Invalid or absent IDs are replaced with a 32-character hexadecimal ID. The API uses the TCP peer address for `client_ip` and sanitizes and truncates user agents. Access logs omit authorization and cookie headers, query strings, and request bodies. Structured attributes named `Authorization`, `token`, `bearer`, `password`, `secret`, `api_key`, `apikey`, `cookie`, `dsn`, or `private_key` are masked case-insensitively, including compound keys such as `GITHUB_TOKEN`. The database startup target includes only the URL scheme, host and database path for a parsed PostgreSQL URL. Avoid placing credentials in caller-supplied paths, request IDs, or user-agent text.

Compose uses the `json-file` logging driver with three 10 MB files per service. Use `docker compose logs --tail=200 api worker` to inspect a deployment with an external worker, or `docker compose logs --tail=200 api` for the embedded topology.

| Endpoint | Semantics |
| --- | --- |
| `GET /healthz` | Liveness returns 200 with service metadata, a UTC timestamp, and uptime. |
| `GET /readyz` | Returns 200 when PostgreSQL is reachable and 503 when PostgreSQL is unavailable. `checks.redis` is `ok`, `degraded`, or `disabled`. Redis outage leaves readiness at 200 when PostgreSQL works. |
| `GET /openapi.yaml`, `GET /openapi.json` | Public copies of the same embedded OpenAPI contract. |

## API security

- Requests to `/api/v1/*` use one Bearer credential when authentication is enabled. The service returns `401` and `WWW-Authenticate: Bearer realm="ProjectLens"` for missing or invalid credentials. CORS preflight and public probes bypass Bearer auth.
- Use a CORS origin allowlist. Browsers can send `Authorization`, `Content-Type`, and `X-Request-ID` with the default header configuration. For cross-origin conditional writes, add `If-Match` to `cors.allowed_headers` in the service YAML.
- Responses set `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and `Referrer-Policy: no-referrer`. API responses also set `Cache-Control: no-store`.
- Local Git paths must resolve under an absolute path in `REPO_ALLOWED_LOCAL_ROOTS`, including their Git directories. Mount explicitly allowed working trees read-only and ensure that Git can read and trust their ownership as the scanner user. An empty allowlist denies local imports.
- GitHub import accepts public GitHub repository URLs. `GITHUB_GIT_BASE_URL` configures a trusted HTTP(S) mirror for Git fetches. `GITHUB_API_BASE_URL` configures the metadata endpoint. The GitHub token is scoped to GitHub fetches and is omitted on Git fetches to a mirror.
- The global HTTP body and raw document JSON limits return `413 payload_too_large` when exceeded. The decoded Markdown content limit is measured in UTF-8 bytes and returns `400 invalid_input` when exceeded. Git limits bound file count, bytes, and path shape during scans.

## Tune timeouts and resource limits

| Setting | Default | Tuning |
| --- | --- | --- |
| `SERVER_READ_HEADER_TIMEOUT`, `SERVER_READ_TIMEOUT` | 5s each | Header receipt and full request reading, including the body |
| `SERVER_REQUEST_TIMEOUT`, `SERVER_WRITE_TIMEOUT` | 8s and 10s | Set the cooperative handler deadline below the write timeout to leave time to write a `504` |
| `SERVER_IDLE_TIMEOUT`, `SERVER_SHUTDOWN_TIMEOUT` | 60s and 10s | HTTP keep-alive idle time and graceful drain |
| `SERVER_MAX_HEADER_BYTES` | 65536 | Allowed range 1 KiB to 1 MiB. net/http rejects oversized headers before routing. |
| `SERVER_MAX_BODY_BYTES` | 4194304 | Allowed range 1 KiB to 64 MiB. A larger limit also raises peak memory for concurrent writes. |
| `SERVER_MAX_IN_FLIGHT` | 128 | Allowed range 0 to 10000. Zero disables shedding. Overload returns 503 and `Retry-After: 1`. Health and readiness probes are exempt. |
| `DB_MIN_CONNS`, `DB_MAX_CONNS` | 2 and 10 | Budget PostgreSQL connections across every replica and worker. Use `DB_CONNECT_TIMEOUT` to bound startup connection checks. |
| `REDIS_DIAL_TIMEOUT`, `REDIS_READ_TIMEOUT`, `REDIS_WRITE_TIMEOUT` | 300ms each | Keep cache operations short enough to leave time for PostgreSQL fallback. |
| `SCANNER_GIT_TIMEOUT`, `SCANNER_MAX_FILE_SIZE`, `SCANNER_MAX_TOTAL_BYTES`, `SCANNER_MAX_FILES` | 2m, 1 MiB, 50 MiB, 10000 | Size Git and scan limits with worker concurrency. Use path depth and length limits and exclude patterns to bound large trees. |

Compose caps each API and worker container at 2 CPUs, 768 MiB RAM, and 256 PIDs. Tune these limits alongside worker concurrency and database pool size. A request deadline propagates cancellation through the request context. The timeout response requires the handler to return before its response has been written.

## Diagnose and recover

### PostgreSQL unavailable

Expect `/readyz` to return 503 while `/healthz` remains 200 on a running API. Remove the instance from traffic and check database health, DNS, connection limits, TLS and credentials. Restore connectivity and wait for readiness before restoring traffic. A newly starting API requires a reachable database.

### Redis unavailable

Confirm `checks.redis=degraded` and `X-Search-Cache: degraded`. Search continues through PostgreSQL. Watch database load, restore Redis, then check readiness and subsequent searches. On an empty cache, repeated identical searches report `miss` then `hit`. Project search versions in PostgreSQL invalidate older cached results after writes.

### Worker interrupted or jobs stuck

Inspect `GET /api/v1/jobs/{id}` for status, attempt count, lease expiry, and last error code. Check that workers connect to the right database and can read local mounts or reach the Git mirror. A worker reclaims expired leases after `WORKER_LEASE_DURATION`; exhausted attempts end in failure. Keep `WORKER_HEARTBEAT_INTERVAL` below that lease duration. Retry resolved failures by enqueueing a new job with a new idempotency key. Use the cancel endpoint for pending or running jobs that should stop.

### Backup, restore and rollback

Back up PostgreSQL with `pg_dump -Fc` or the database provider's backup tooling. Test restoration with `pg_restore` into a clean database, check the migration ledger, and verify project, document, snapshot, and job reads before cutting over. Protect backup credentials and test restore access. Keep local Git sources available for future scans. Redis and the remote Git cache can be recreated after a restore.

For rollback, stop new writes and drain workers. Deploy the previous image if its expected schema is compatible with the applied migration. If a schema migration must be reversed, take a fresh backup, verify the migration's down behavior, and use the matching release's `/app/migrate down` when approved for the data it may remove. Restore the matching database backup when the rollback must recover dropped data. Check readiness, auth, and a representative search and job before resuming traffic.

## Delivery checks

Run `make test` for race-enabled Go tests. With `TEST_DATABASE_URL` set to a PostgreSQL test instance, `make test-integration` exercises the integration suites and `make test-e2e` runs full delivery against real PostgreSQL with isolated Redis and GitHub fixtures. Run `make docker-e2e` on a Docker host to check the clean Compose deployment, token handling, local and remote ingestion, search, Redis outage and recovery, and API restart persistence. The script removes its Compose project and volumes when it exits.


## Repository synchronization

The manual endpoints and automatic scheduler acquire the repository row in PostgreSQL before checking active jobs and inserting a new one. A worker acquires the existing repository advisory lock before scanning or reconciling. Deploy the schema and replace all API and worker instances together. An older process can still register mock handlers or use the earlier enqueue behavior. In production and staging, persisted mock jobs are claimed and finish with `unknown_job_type`. Inspect their job status through the existing read API.

Commit synchronization pins a tip, imports up to 100 reachable entries initially and expands the published window by up to 100 positions on subsequent runs, to 2,000 positions per ref. A force-push rebuilds the reachability window from the new tip. SHA details for older imported objects remain in PostgreSQL. The worker computes metadata only for missing SHAs. The published tip, ordered SHA list and new records commit in one transaction. Compare reads actual Git objects, which requires the base and head trees to remain available in the local repository or managed Git cache. Retain that cache when comparisons across rewritten history matter. Git command timeouts also bound Compare. File metadata is limited to 500 changes, each Git metadata stream to 256 KiB and each patch and saved message body to 64 KiB. Check `truncated` and `patch_truncated`. A dumb HTTP Git mirror can require a full transfer when shallow fetch is unsupported.

GitHub activity checks public visibility and native repository identity before every batch. The worker uses issue records to discover PR numbers and fetches the actual PR IDs. It reads the repository-wide ordinary and review comment streams separately. PR detail ETags are persisted with the normalized record. Data and the upstream update-time cursor commit together. Incremental scans overlap by two minutes. The first successful scan and the first sync after 24 hours since reconciliation enumerate all pages. Only a successful full reconciliation removes missing rows. A changed PR refreshes its complete commit membership. Page or size limits, inconsistent PR counts and transport errors leave the previous published batch intact.

Each activity run permits 256 list/detail requests plus the visibility check, 50 pages per list, 100 entries per page, 4 MiB per activity response and 32 MiB across those responses. Stored bodies retain 16,384 characters, with a truncation flag. A PR above GitHub's 250-commit REST limit, or a repository exceeding the batch limits, surfaces a job error. Check the job's error before retrying an oversized import. For a rate limit, inspect `last_error_code=github_rate_limit` and `run_at`. The worker honors the longer of its bounded exponential retry delay and the upstream retry/reset deadline. A later scheduler tick also checks the persisted deadline. Verify credentials and repository visibility for permanent permission or not-found errors. The REST behavior is documented by [GitHub Issues](https://docs.github.com/en/rest/issues/issues), [Pull Requests](https://docs.github.com/en/rest/pulls/pulls) and [rate-limit guidance](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api).

The scheduler runs with executing workers. A PostgreSQL clock row admits at most `SCHEDULE_BATCH` (default 4) due policies per five-second window across instances. `SCHEDULE_MIN_INTERVAL_SECONDS` defaults to 60. Enabling a new policy makes it due on the next tick. A long outage coalesces missed intervals into one opportunity per policy. Pending jobs are reused and a conflicting manual ref causes the tick to skip that interval. Disable a policy to stop new scheduled jobs. Use the normal cancellation endpoint for an existing job. Deleting a repository cascades its policies and imported data.

## Metrics and tracing

Set `METRICS_ADDR=127.0.0.1:9090` to enable the separate, loopback-only `/metrics` listener. Leave it empty to disable it. Host processes in the same network namespace need distinct ports. In Compose, inspect the API with `docker compose exec api wget -qO- http://127.0.0.1:9090/metrics` and scrape the worker's own loopback listener for its execution series. Database gauges describe the shared database. Choose one scrape target for queue totals to avoid double counting.

| Metric under `projectlens_` | Interpretation |
| --- | --- |
| `http_request_seconds` | Histogram by HTTP status class. Use its count for throughput and the 5xx count for server error rate. |
| `jobs`, `job_attempt_seconds` | Stored job counts by controlled type and state, and attempt duration by type and outcome. Pending includes delayed retries. |
| `job_retries_total`, `job_lease_expiries_total` | Started retries and reclaimed leases. Compare their rates with worker logs. |
| `snapshot_sync_seconds`, `snapshot_files_total`, `snapshot_bytes_total` | Snapshot sync duration and logical scan volume. Fully unchanged snapshots contribute zero scan volume. |
| `github_request_seconds`, `github_retry_wait_seconds` | REST latency/count by status class and upstream-requested retry delay in seconds. |
| `search_seconds` | Search latency split by hit, miss, bypass, degraded or error. Compare the degraded and miss buckets during a Redis outage. |
| `db_connections`, `db_scrape_up` | Application pool connections by state and availability of the PostgreSQL job count query. |

Example PromQL for service-wide request P95 and server error fraction:

```promql
histogram_quantile(0.95, sum by (le) (rate(projectlens_http_request_seconds_bucket[5m])))
sum(rate(projectlens_http_request_seconds_count{status_class="5xx"}[5m])) / sum(rate(projectlens_http_request_seconds_count[5m]))
```

Set `TRACE_ENABLED=true` on API and worker to emit local OpenTelemetry span summaries to structured logs. The default is off. Trace context travels with the PostgreSQL job. Find the repository enqueue event using `X-Request-ID`, then filter API and worker logs by its `trace_id` or `job_id`. The spans cover HTTP, enqueue, worker execution, Git commands, GitHub REST requests, SQL queries, snapshot publication and search. The local exporter records span names, parents, IDs and duration. Example with JSON logs:

```sh
docker compose logs --no-log-prefix api worker | jq -R 'fromjson? | select(.trace_id == env.TRACE_ID)'
```

Migrations 7–11 add the active-target index, history, GitHub activity, policies and trace context respectively. Keep a V1 backup before rollout. For rollback, disable schedules and drain the newer workers first. Reversing the history and activity migrations removes imported data and cursors. Reversing the scheduler migration removes policies. Restore from backup or re-import the external data and recreate policies after upgrading again.
