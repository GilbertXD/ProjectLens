# ProjectLens

ProjectLens is a Go 1.25 HTTP service built with Gin.

## Quickstart with Docker Compose

From a clean checkout with Docker and the Compose plugin, start the API, PostgreSQL, Redis, and the embedded worker. Compose accepts shell overrides and works without an environment file.

```sh
docker compose --env-file /dev/null up --build -d
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/readyz
export PROJECTLENS_TOKEN=projectlens-dev-token
curl -fsS -H "Authorization: Bearer $PROJECTLENS_TOKEN" http://localhost:8080/api/v1/ping
```

Compose enables authentication and uses a public development token and database password. Configure production credentials, TLS, and port exposure before opening access. Stop the stack with `make docker-down`. The named PostgreSQL volume retains application data.

## Run locally

Start PostgreSQL and Redis with `docker compose up -d postgres redis`, or configure existing instances with `DATABASE_URL` and `REDIS_ADDR`. In a terminal with Go and Git installed, run `make run`. Local binaries default to authentication disabled. To use the authenticated examples below, start the server with credentials.

```sh
export PROJECTLENS_TOKEN="$(openssl rand -hex 32)"
export AUTH_ENABLED=true AUTH_BEARER_TOKENS="$PROJECTLENS_TOKEN"
make run
```

The server loads built-in defaults, then YAML, then environment variables. Select a YAML file with `-config` or `APP_CONFIG_FILE`. The server uses `configs/config.yaml` when present and no path is specified. Send Ctrl-C or SIGTERM for graceful shutdown.

| Setting | Purpose |
| --- | --- |
| `AUTH_ENABLED`, `AUTH_BEARER_TOKENS` | API authentication and comma-separated tokens for rotation |
| `API_PORT`, `SERVER_HOST`, `SERVER_PORT` | Compose host port and the local binary's listen address |
| `SERVER_READ_HEADER_TIMEOUT`, `SERVER_READ_TIMEOUT`, `SERVER_WRITE_TIMEOUT`, `SERVER_IDLE_TIMEOUT`, `SERVER_REQUEST_TIMEOUT` | HTTP timeouts and request deadline |
| `SERVER_MAX_HEADER_BYTES`, `SERVER_MAX_BODY_BYTES`, `SERVER_MAX_IN_FLIGHT` | HTTP header, payload, and concurrent request limits |
| `DATABASE_URL`, `DB_AUTO_MIGRATE`, `DB_MAX_CONNS` | PostgreSQL connection, migration policy, and pool size |
| `REDIS_ENABLED`, `REDIS_ADDR`, `REDIS_PASSWORD`, `SEARCH_CACHE_TTL` | Optional search cache |
| `REPO_ALLOWED_LOCAL_ROOTS`, `SCANNER_*` | Allowed local Git paths and scan limits |
| `GITHUB_API_BASE_URL`, `GITHUB_GIT_BASE_URL` | Operator-configured GitHub metadata endpoint and optional Git mirror |
| `CORS_ALLOWED_ORIGINS`, `LOG_LEVEL`, `LOG_FORMAT`, `WORKER_MODE` | Browser origins, logging, and worker topology |

See [.env.example](.env.example) for the full set of local settings and [the operations runbook](docs/OPERATIONS.md) for production deployment, migration and rotation, diagnostics, and recovery.

## API authentication and OpenAPI

Send one `Authorization: Bearer <token>` header on `/api/v1/*` requests when authentication is enabled. Missing or invalid credentials return `401 unauthorized` with a Bearer challenge. Configure more than one token during rotation to keep old clients working while they switch. CORS preflight and the health, readiness, and OpenAPI endpoints are public.

```sh
curl -fsS http://localhost:8080/openapi.yaml
curl -fsS http://localhost:8080/openapi.json
```

Both endpoints serve the embedded OpenAPI 3.0.3 contract. Use either format to explore routes, request bodies, and response schemas.

## Verify

```sh
make vet test build
export TEST_DATABASE_URL='postgres://projectlens:projectlens@localhost:5432/projectlens?sslmode=disable'
make test-integration test-e2e
docker compose config -q
make docker-e2e
```

Database tests use a dedicated schema per test and need a PostgreSQL account that can create schemas. The delivery test starts isolated Redis and GitHub fixtures. The Docker E2E requires Docker Compose, curl, jq, OpenSSL, and access to any build dependencies and images missing locally. It allocates ports and volumes under an isolated project, exercises real API and worker containers, then tears the project down.

## Projects API

The server requires PostgreSQL. For a Go process on the host, provision a database matching `DATABASE_URL` in `.env.example` or export a DSN for an existing database. Compose starts PostgreSQL and stores application data in the named `postgres_data` volume.

The server applies embedded migrations on startup by default. Set `DB_AUTO_MIGRATE=false` if you apply migrations separately using `make migrate-up`. Use `make migrate-status` to inspect migration state. `make migrate-down` rolls back one migration at a time. Migration 12 adds repository roles and document ownership. Rolling it back preserves repository and document rows and removes those two fields. Migration 13 adds `documents.metadata` and changes document ownership to survive disassociation. Rolling it back restores the previous behaviour, which deletes repository-owned documents along with the repository.

| Method | Path | Result |
| --- | --- | --- |
| POST | `/api/v1/projects` | Create a project and return 201 |
| GET | `/api/v1/projects` | List projects with optional `status`, `limit` (default 20), and `offset` (default 0) |
| GET | `/api/v1/projects/:id` | Get a project by UUID |
| PUT | `/api/v1/projects/:id` | Replace the project's fields |
| PATCH | `/api/v1/projects/:id` | Update the supplied fields |
| DELETE | `/api/v1/projects/:id` | Delete a project and return 204 |

Use `slug`, `name`, `description`, and `status` in JSON request bodies. Status is `active` or `archived` and defaults to `active` on create. PUT requires a name, slug, and status. PATCH requires at least one field. List responses contain `items`, `total`, `limit`, and `offset`.

```sh
curl -fsS http://localhost:8080/api/v1/projects \
  -H "Authorization: Bearer $PROJECTLENS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"slug":"example-project","name":"Example Project"}'
```

## Unified Search API

Search a project's active Git snapshots and its project- and repository-level Markdown documents with `GET /api/v1/projects/:id/search`.

```sh
curl -G "http://localhost:8080/api/v1/projects/$PROJECT_ID/search" \
  -H "Authorization: Bearer $PROJECTLENS_TOKEN" \
  --data-urlencode 'q="connection pool" OR timeout' \
  --data-urlencode 'source_type=local_git,github,document' \
  --data-urlencode 'limit=20' \
  --data-urlencode 'offset=0'
```

- `q` is required and defaults to a maximum of 200 characters. Use quotes for phrases, `OR` for alternatives, and a minus sign to exclude a term.
- `source_type` accepts `local_git`, `github`, and `document`. Repeat the parameter or separate the values with commas. By default, all source types are included.
- Use `GET /api/v1/projects/:id/repositories/:repoId/search` or pass `repository_id` to project search to retrieve a single repository’s files and documents.
- Use `scope=project` for project-level documents. Use `scope=cross_repository` to search repositories explicitly, with optional `repository_ids` (repeat the parameter or use comma-separated IDs) and `repository_role=primary|reference`. Either repository filter also selects cross-repository search. Add `include_project_documents=true` to include project-level documents in those results.
- `path_prefix` limits results to files whose paths begin with the literal prefix. A repository ID outside the project returns `404`.
- `limit` defaults to 20 and accepts up to 100. `offset` defaults to zero. Pages include the full match `total` and an empty `items` array when the offset is beyond the last match.

Each hit has a relevance score, a snippet with HTML-escaped text and `<mark>` highlights, and a `location.api_path` to fetch the full content. File locations include repository, snapshot, commit, and path information. GitHub hits include a link pinned to the indexed commit. Locations include `scope` and, for repository-owned results, `repository_id`, `repository_name`, `repository_role`, and `repository_source`. Document locations also include the document ID, title, and version. A content match includes its first matching line when available.

Redis is enabled by default. Compose starts Redis, publishes port 6379 by default (`REDIS_PORT`), and connects the API to `redis:6379`. A server running on the host uses `localhost:6379` by default. If you use `.env.example` as a Compose environment file, set `REDIS_ADDR=redis:6379` for the container. Set `REDIS_ENABLED=false` to bypass the cache, or configure `REDIS_*` and `SEARCH_*` using the values in `.env.example`. Cached results expire after five minutes by default. Search reads the project's version from PostgreSQL so writes that change searchable content or provenance invalidate older results. Responses include `X-Search-Version` and `X-Search-Cache`, whose value is `hit`, `miss`, `bypass`, or `degraded`. Redis errors fall back to PostgreSQL and are reported as `checks.redis=degraded` at `/readyz`.

With `TEST_DATABASE_URL` set, the search integration tests use an in-process Redis server by default. Set `TEST_REDIS_URL` to exercise caching against an external Redis instance. Outage and recovery tests use their own isolated Redis server.

## Markdown Documents API

Markdown documents are owned by the project or by one of its repositories. At `/api/v1/projects/{projectId}/documents`, omit `repository_id` on create for a project-level document, or supply it for a repository-owned document. The response reports `scope` as `project` or `repository`.

Documents are user-authored notes, so disassociating a repository never deletes them. A repository-owned document becomes a project-level document and reports `scope` as `project` with an `origin` object holding `repository_id` and `repository_name`. Provenance is informational only: it never grants repository scope. PUT replaces it (omitting `origin` clears it) and PATCH sets it, clears it with `null`, or leaves it unchanged when omitted.

The project list includes both scopes by default. Filter with `scope=project`, `scope=repository`, or `repository_id=<uuid>`. A repository ID is incompatible with `scope=project`. Use `/api/v1/projects/{projectId}/repositories/{repoId}/documents` as the base path to create, list, read, update, and delete documents owned by one repository. The repository is taken from the path on create. Requests for repositories or documents outside the specified scope return `404`.

| Method | Path relative to base | Behavior |
| --- | --- | --- |
| POST | (base) | Create with `title` and optional `content`. Returns `201`. |
| GET | (base) | List summaries without full content. Returns `200`. |
| GET | `/{docId}` | Read the full document. Returns `200`. |
| PUT | `/{docId}` | Replace the title, content, and `origin`. Omitted content becomes empty and omitted `origin` is cleared. Returns `200`. |
| PATCH | `/{docId}` | Change `title`, `content`, and `origin`, or any combination. Send `"origin": null` to clear provenance. Returns `200`. |
| DELETE | `/{docId}` | Delete the document. Accepts an optional `If-Match`. Returns `204`. |

Lists are ordered by most recently updated, then by descending document ID. Use `limit` (default 20, maximum 100) and `offset` (default 0). The response includes `items`, `total`, `limit`, and `offset`. Requests for documents outside the specified project return `404`.

New documents start at version 1. Successful creates, reads of a single document, and updates return a numeric `version` and a quoted `ETag`, such as `"1"`. PUT and PATCH require a JSON `version`, an `If-Match` header, or both with the same value. The header accepts `"1"`, `W/"1"`, or `1`. An update increments the version atomically. A stale version on an update or conditional delete returns `409 version_conflict`. Read the latest document before retrying. Missing, invalid, or mismatched version inputs on updates return `400 invalid_input`.

```sh
curl -X PATCH "$BASE/$DOCUMENT_ID" \
  -H "Authorization: Bearer $PROJECTLENS_TOKEN" \
  -H 'Content-Type: application/json' -H 'If-Match: "1"' \
  -d '{"content":"# Updated notes"}'
```

Titles are trimmed and must contain 1 to 200 characters. Markdown content may be empty. Set `document.max_content_bytes` in YAML or override it with `DOCUMENT_MAX_CONTENT_BYTES`. The default and PostgreSQL ceiling are 1,048,576 UTF-8 bytes (1 MiB). Documents report the byte count as `content_bytes`. Decoded content over the configured limit returns `400 invalid_input`. The API also limits raw JSON request size to the greater of 256 KiB and twice the content limit plus 64 KiB. Requests above the raw document limit or the global `SERVER_MAX_BODY_BYTES` cap return `413 payload_too_large`.

## Repository Association API

A project can associate local Git working trees and public GitHub repositories. Set `role` to `primary` (the default) or `reference`. A project can have multiple repositories of either role. Local and GitHub associations keep independent snapshots, history, activity, and notes even when they share a name, remote URL, or commit SHA. The same repository can be associated with different projects. Within one project, symlink aliases for local paths and equivalent GitHub URLs resolve to the same canonical source.

| Method | Path under `/api/v1/projects/:id` | Result |
| --- | --- | --- |
| POST | `/repositories` | Associate a repository and return 201 |
| GET | `/repositories` | List repositories with optional `role`, `source_type`, `limit` (default 20, maximum 100), and `offset` (default 0) |
| GET | `/repositories/:repoId` | Get repository metadata |
| PATCH | `/repositories/:repoId` | Update `name`, `description`, or `role` |
| POST | `/repositories/:repoId/sync` | Queue a snapshot (optional JSON `{"ref":"main"}`). Poll the returned `poll_url`. Same active ref returns 200, conflicting ref returns 409. |
| POST | `/repositories/:repoId/refresh-metadata` | Refresh source metadata and set `last_synced_at`, preserving the name and description |
| DELETE | `/repositories/:repoId` | Remove the association and return 204 |

Disassociation deletes that repository’s snapshots, files, history, GitHub activity, and sync policies. Pending jobs for the repository are cancelled and running jobs receive a cancellation request. The project’s search cache is invalidated. Documents survive as project-level documents that keep the repository under `origin`, and data owned by other repositories is preserved.

POST takes a `source_type` of `local_git` or `github`, a `source`, and optional `name`, `description`, and `role`. For GitHub, use a public repository URL such as `https://github.com/owner/repo`. For local Git, use an absolute path to the root of a readable working tree. The server needs Git installed and access to the path. In Docker Compose, bind-mount the repositories and set the allowed roots to their paths inside the container. Local associations are forbidden by default.

```sh
curl -fsS "http://localhost:8080/api/v1/projects/$PROJECT_ID/repositories" \
  -H "Authorization: Bearer $PROJECTLENS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"source_type":"github","source":"https://github.com/owner/repo"}'
```

List responses contain `items`, `total`, `limit`, and `offset`. Error responses contain `error` and `code`. Invalid sources return 400, forbidden sources return 403, missing projects or repositories return 404, and duplicate associations return 409. GitHub rate limits return 429 and upstream failures return 502.

| YAML key in `configs/config.yaml` | Environment override | Default |
| --- | --- | --- |
| `repository.allowed_local_roots` | `REPO_ALLOWED_LOCAL_ROOTS` (comma-separated absolute paths) | Empty list |
| `github.api_base_url` | `GITHUB_API_BASE_URL` | `https://api.github.com` |
| `github.token` | `GITHUB_TOKEN` | Empty (anonymous requests) |
| `github.timeout` | `GITHUB_TIMEOUT` | `10s` |
| `github.user_agent` | `GITHUB_USER_AGENT` | `ProjectLens` |

### Source repository security

ProjectLens reads local Git and public GitHub repositories into controlled PostgreSQL snapshots. Standalone Markdown documents are editable in PostgreSQL. Deleting a repository association or its project leaves the source repository intact. The only Git writes are to ProjectLens's dedicated bare cache for remote fetches.

Compose runs the API, worker, and migrator with read-only root filesystems (`read_only: true`), all Linux capabilities dropped (`cap_drop: [ALL]`), and a `noexec,nosuid` tmpfs at `/tmp`. The local repository override mounts the host sources read-only at `/repos` on both the API and worker. Native deployments without OS-level read-only mounts or read-only permissions for the service account rely on application-level guards. [Compose example](docker-compose.local-repos.example.yml) · [Deployment details](docs/OPERATIONS.md#source-repository-isolation)

Use GitHub anonymously or set `GITHUB_TOKEN` to a fine-grained read-only token or a zero-scope classic personal access token. The GitHub API client rejects elevated classic token scopes at runtime.

## Background jobs

The API writes jobs to PostgreSQL and returns immediately. By default, the server runs an embedded worker. Set `WORKER_MODE=external` to run the API and worker as separate processes, or `WORKER_MODE=disabled` to run the API without a local worker. Both API-only modes still accept jobs. Pending jobs wait for an available worker.

```sh
make build
WORKER_MODE=external make run
# In a separate terminal, using the same database:
make run-worker

# Or run the API and a separate worker in Compose:
WORKER_MODE=external docker compose --profile worker up --build -d
```

| Variable | Default | Purpose |
| --- | --- | --- |
| `WORKER_MODE` | `embedded` | Server startup mode |
| `WORKER_ID` | Generated UUID | Worker identity override |
| `WORKER_CONCURRENCY` | `4` | Concurrent executions per worker |
| `WORKER_POLL_INTERVAL` | `500ms` | Queue and expired-lease polling interval |
| `WORKER_LEASE_DURATION` | `30s` | Duration of a claim or renewal |
| `WORKER_HEARTBEAT_INTERVAL` | `10s` | Lease renewal interval, shorter than the lease duration |
| `WORKER_DEFAULT_TIMEOUT` | `5m` | Execution timeout per attempt, stored in whole seconds |
| `WORKER_DEFAULT_MAX_ATTEMPTS` | `3` | Total attempts, including the initial execution |
| `WORKER_RETRY_BASE_DELAY` | `2s` | Initial failure retry delay and delay after lease recovery |
| `WORKER_RETRY_MAX_DELAY` | `1m` | Cap on exponential backoff after execution failures |
| `WORKER_SHUTDOWN_TIMEOUT` | `15s` | Time to drain active executions before cancelling their contexts |

```sh
curl -X POST http://localhost:8080/api/v1/jobs \
  -H "Authorization: Bearer $PROJECTLENS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"type":"mock.sleep","payload":{"duration_ms":1500},"idempotency_key":"demo-sleep"}'
```

- `POST /api/v1/jobs` returns `202` with the new job. Reusing a key with the same type and equivalent JSON payload returns the existing job with `200`. A different type or payload returns `409`. Optional fields include `max_attempts`, `timeout_seconds`, and an RFC 3339 `run_at`.
- `GET /api/v1/jobs` accepts `status`, `type`, `limit` (default 20, maximum 100), and `offset`. Use `GET /api/v1/jobs/:id` to inspect the status and result.
- `POST /api/v1/jobs/:id/cancel` cancels pending jobs immediately and requests cooperative cancellation for running jobs. Cancelling a cancelled job returns its current state. Succeeded and failed jobs return `409`.

With `APP_ENV=development` or `test` and `WORKER_ENABLE_MOCK_JOBS=true`, the available mock types are `mock.echo` (optional `delay_ms`), `mock.sleep` (`duration_ms`, default 100), `mock.fail` (optional `delay_ms`, `error_message`, and `permanent`), and `mock.flaky` (`succeed_on_attempt`, default 2).

A worker renews its lease while an attempt runs. An expired lease lets another worker retry the job if attempts remain. A cancellation request takes precedence during recovery. Job handlers should honor context cancellation and make external side effects safe to repeat because a worker can lose its lease after performing work. The enqueue idempotency key deduplicates job creation.

### Scan a repository

Associate the repository with a project, then submit a scan through the jobs API.

```sh
curl -X POST http://localhost:8080/api/v1/jobs \
  -H "Authorization: Bearer $PROJECTLENS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"type":"repository.scan","payload":{"project_id":"<project UUID>","repository_id":"<repository UUID>","ref":"main"}}'
```

Omit `ref` to scan `HEAD`. A ref can also be a branch, tag, or commit SHA. The job result contains commit metadata, a sorted manifest with Git blob IDs and SHA-256 hashes, skip reasons, and scan totals. It contains no file contents. Local scans use committed Git objects, including when the index or working tree has pending changes. Local repositories, Git directories, and alternate object stores must be inside the worker's `REPO_ALLOWED_LOCAL_ROOTS`. With Docker Compose, supply the container path under the read-only bind mount.

Public GitHub scans fetch into a bare cache owned by the worker. `GITHUB_TOKEN` is optional. Configure the scanner via the `scanner` YAML section or these environment variables:

| YAML key | Environment variable | Default |
| --- | --- | --- |
| `cache_dir` | `SCANNER_CACHE_DIR` | `projectlens-git-cache` under the OS temp directory |
| `git_timeout` | `SCANNER_GIT_TIMEOUT` | `2m` per scan |
| `max_file_size` | `SCANNER_MAX_FILE_SIZE` | `1048576` bytes |
| `max_total_bytes` | `SCANNER_MAX_TOTAL_BYTES` | `52428800` bytes |
| `max_files` | `SCANNER_MAX_FILES` | `10000` |
| `max_path_length` | `SCANNER_MAX_PATH_LENGTH` | `512` bytes |
| `max_path_depth` | `SCANNER_MAX_PATH_DEPTH` | `32` path components |
| `exclude_patterns` | `SCANNER_EXCLUDE_PATTERNS` | Empty |

Custom exclusions add to the built-in exclusions for paths such as `node_modules`, `vendor`, and `dist`. Specify comma-separated Go path globs in the environment. A pattern without a slash matches individual path components. A pattern with a slash matches paths relative to the repository root, including parent directories.

Set `TEST_SCAN_LOCAL_REPO` and `TEST_SCAN_REMOTE_REPO` to exercise the live scanner smoke tests with `go test -run '^TestLive' ./internal/gitscan`.

### Publish a repository snapshot

Call the repository sync endpoint to publish its scanned source files and commit metadata to PostgreSQL.

```sh
curl -X POST "http://localhost:8080/api/v1/projects/$PROJECT_ID/repositories/$REPO_ID/sync" \
  -H "Authorization: Bearer $PROJECTLENS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"ref":"main"}'
```

Omit `ref` to sync `HEAD`. The job uses the same Git sources and filters as `repository.scan`. The job result includes `status`, `snapshot_id`, `version`, commit and tree hashes, file change counts, `blobs_read`, and `pruned_snapshots`. A matching commit, tree, and filter configuration returns `unchanged` with zero blob reads and the existing snapshot ID. A changed scan returns `published` with a new version. Git reads new blobs and PostgreSQL copies content for files whose blobs already appear in the parent snapshot. The previous version serves reads until the new version commits.

Query snapshots and active files under `/api/v1/projects/:id/repositories/:repoId`.

| Method and path | Result |
| --- | --- |
| `GET /snapshots` | Snapshot history, newest version first. Accepts `limit` and `offset`. |
| `GET /snapshots/active` | The active snapshot's metadata. Returns `404` before the first publish. |
| `GET /files` | Active file metadata ordered by path. `q` matches a case-insensitive literal substring in the path or content. `path_prefix` matches the literal start of the path. Accepts `limit` and `offset`. |
| `GET /files/content?path=src%2Fmain.go` | Metadata and text content for an exact active file path. Returns `404` for missing files. |

Paginated responses include `items`, `total`, `limit`, and `offset`. The default limit is 20, the maximum is 100, and the default offset is 0. File search returns an empty page for a repository awaiting its first snapshot.

| YAML key under `sync` | Environment variable | Default |
| --- | --- | --- |
| `retain_superseded` | `SYNC_RETAIN_SUPERSEDED` | `2` superseded versions per repository. Use `0` to keep only the active published version. |
| `failed_retention` | `SYNC_FAILED_RETENTION` | `24h` for failed and stale staging snapshots. |

Successful syncs, including unchanged runs, apply retention. Pruning a snapshot deletes its associated files. Repository sync locks use a separate pool with up to `DB_MAX_CONNS` sessions per process so locking can proceed alongside publishes and API reads.

With `TEST_DATABASE_URL` set, live snapshot sync smoke tests use the same source variables as the live scanner tests. For example:

```sh
TEST_SCAN_LOCAL_REPO=/Users/hiya/HyAgent \
TEST_SCAN_REMOTE_REPO=https://github.com/GilbertXD/HyMail.git \
  go test -race -v -run '^TestLiveSnapshotSync$' ./internal/integration
```


### Commit history, GitHub activity and schedules

Set `R=/api/v1/projects/$PROJECT_ID/repositories/$REPO_ID` for an associated repository. Send the usual Bearer header. A newly queued sync returns 202 with `job_id`, `status` and `poll_url`. Poll that URL or use the existing job cancellation endpoint. An active sync of the same type and ref returns 200. A competing ref returns 409.

| Method | Path | Result |
| --- | --- | --- |
| POST | `$R/sync`, `$R/commits/sync` | File snapshot or commit history. Optional body `{"ref":"main"}`, default `HEAD`. |
| POST | `$R/refresh-metadata` | Synchronously refresh repository metadata. Clients using the old metadata-only `/sync` behavior should migrate here. |
| GET | `$R/commits?ref=main` | Published tip, import window and topologically ordered commits. |
| GET | `$R/commits/:sha`, `$R/commits/:sha/files` | Imported metadata or first-parent changes. A root commit is compared with an empty tree. |
| GET | `$R/compare?base=<sha>&head=<sha>` | File changes, binary flags, rename paths, line counts and a bounded unified patch. Both full SHAs must already belong to this repository's imported history. |
| GET | `$R/commits/:sha/tree?path_prefix=src/` | Regular files at a full commit SHA. Filter by an exact path or directory prefix. |
| GET | `$R/commits/:sha/file?path=src/main.go` | File metadata, SHA-256 checksum and UTF-8 text at a full commit SHA. Binary files return metadata with `binary: true`. |
| GET | `$R/files/history?path=src/main.go&ref=main&follow=true` | Commits that changed a file. Set `follow=true` to track renames. |
| GET | `$R/files/blame?path=src/main.go&ref=main&start_line=1&end_line=20` | Line attribution, including original filename, line number and Git boundary flags. |
| GET | `$R/refs?type=tag` | Branches, tags, the default branch and peeled annotated tag hashes. Omit `type` to list both branches and tags. |
| POST | `$R/github-activity/sync` | Synchronize public GitHub issues, PRs, comments, review comments and associated commits. |
| GET | `$R/issues`, `$R/pulls` | Repository activity lists. Project-wide lists are available at `/api/v1/projects/:id/issues` and `/api/v1/projects/:id/pulls`. |
| GET | `$R/issues/:number`, `$R/pulls/:number` | Issue or PR details. Append `/comments` for discussion and review comments, or `/commits` for a PR's commit links. |
| GET | `$R/sync-policy` | Configured policies, next due time, latest job, last success time and recent failure code. A missing policy is disabled. |
| PUT | `$R/sync-policy/:kind` | Set a policy, e.g. `{"enabled":true,"interval_seconds":3600}`. Kinds are `files`, `commits` and `github_activity`. |

Lists accept `limit` (default 20, maximum 100) and nonnegative `offset`. File pagination in Compare applies to `items`. The patch describes the full comparison. Job results and stored data survive API and worker restarts. Commit history and activity use the scoped sync endpoints. See the [operations guide](docs/OPERATIONS.md) and [measured performance report](docs/PERFORMANCE.md).

Commit history sync expands the imported ref window in steps of 100 up to 2,000 commits. The job result and `GET $R/commits` include `window_limit`, `page_size`, `complete`, `truncated`, `window_limit_reached` and `window_status`. A `partial` window can expand on the next sync. `window_limit_reached` means older commits exist past the cap. `complete` means the window contains all reachable commits.

Historical tree, file, history, blame and ref queries read directly from Git. They can use commits outside the import window without changing the active snapshot. File history and blame default to `HEAD`. Blame accepts an inclusive pair of 1-based line numbers spanning at most 2,000 lines. With no range, it returns up to the first 2,000 lines and indicates whether it capped the output. File content and blame enforce the configured maximum file size.

For GitHub repositories, pinned tree and file reads can use a cached commit while the remote is offline. Git fetches missing commits as needed. File history and blame may deepen a shallow cache and report remaining shallow boundaries. A missing commit or path returns `404`, an upstream rate limit returns `429`, and a transport failure when Git needs the remote returns `502`.
