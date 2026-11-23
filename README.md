# ProjectLens

ProjectLens is a Go 1.25 HTTP service built with Gin.

## Run locally

Start PostgreSQL and configure the database connection as described below. Start the server, then run the `curl` commands from another terminal.

```sh
make run
curl -fsS http://localhost:8080/healthz
curl -fsS http://localhost:8080/api/v1/ping
```

`GET /healthz` reports service metadata, the current UTC timestamp, and uptime as a Go duration string. `GET /api/v1/ping` returns `{"message":"pong"}`. Send `SIGINT` or `SIGTERM` to shut down gracefully.

The service starts with built-in defaults and applies a YAML file followed by environment overrides. Select the file with `-config`, which takes precedence over `APP_CONFIG_FILE`. When neither is set, the server uses `configs/config.yaml` if present. An explicitly selected file must exist. See `.env.example` for all supported environment variables. For local runs, export the variables in your shell. To enable CORS, set `CORS_ALLOWED_ORIGINS` to a comma-separated list or `*`.

## Run with Docker Compose

```sh
make docker-up
curl -fsS http://localhost:8080/healthz
make docker-down
```

Compose publishes port 8080 and defaults to production mode with JSON logs. It reads supported overrides from the shell or a local `.env` file.

## Verify

```sh
make vet test build
docker compose config -q
```

## Projects API

The server requires PostgreSQL. For a Go process on the host, provision a database matching `DATABASE_URL` in `.env.example` or export a DSN for an existing database. Compose starts its own PostgreSQL instance and stores data in the named `postgres_data` volume. `GET /readyz` reports database connectivity, and `GET /healthz` continues to report process health.

The server applies embedded migrations on startup by default. Set `DB_AUTO_MIGRATE=false` if you apply migrations separately using `make migrate-up`. Use `make migrate-status` to inspect migration state. `make migrate-down` rolls back one migration at a time. From the latest version, the first rollback drops the repositories table and the second drops the project and audit tables.

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
  -H 'Content-Type: application/json' \
  -d '{"slug":"example-project","name":"Example Project"}'
```

Set `TEST_DATABASE_URL` to a PostgreSQL database where the test user can create schemas, then run `make test-integration`. Each test creates and drops its own schema. With the variable unset, the PostgreSQL tests skip.

## Repository Association API

A project can associate local Git working trees and public GitHub repositories. The same repository can be associated with different projects. Within one project, symlink aliases for local paths and equivalent GitHub URLs resolve to the same canonical source.

| Method | Path under `/api/v1/projects/:id` | Result |
| --- | --- | --- |
| POST | `/repositories` | Associate a repository and return 201 |
| GET | `/repositories` | List repositories with optional `source_type`, `limit` (default 20, maximum 100), and `offset` (default 0) |
| GET | `/repositories/:repoId` | Get repository metadata |
| PATCH | `/repositories/:repoId` | Update `name`, `description`, or both |
| POST | `/repositories/:repoId/sync` | Refresh source metadata and set `last_synced_at`, preserving the name and description |
| DELETE | `/repositories/:repoId` | Remove the association and return 204 |

POST takes a `source_type` of `local_git` or `github`, a `source`, and optional `name` and `description`. For GitHub, use a public repository URL such as `https://github.com/owner/repo`. For local Git, use an absolute path to the root of a readable working tree. The server needs Git installed and access to the path. In Docker Compose, bind-mount the repositories and set the allowed roots to their paths inside the container. Local associations are forbidden by default.

```sh
curl -fsS "http://localhost:8080/api/v1/projects/$PROJECT_ID/repositories" \
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
  -H 'Content-Type: application/json' \
  -d '{"type":"mock.sleep","payload":{"duration_ms":1500},"idempotency_key":"demo-sleep"}'
```

- `POST /api/v1/jobs` returns `202` with the new job. Reusing a key with the same type and equivalent JSON payload returns the existing job with `200`. A different type or payload returns `409`. Optional fields include `max_attempts`, `timeout_seconds`, and an RFC 3339 `run_at`.
- `GET /api/v1/jobs` accepts `status`, `type`, `limit` (default 20, maximum 100), and `offset`. Use `GET /api/v1/jobs/:id` to inspect the status and result.
- `POST /api/v1/jobs/:id/cancel` cancels pending jobs immediately and requests cooperative cancellation for running jobs. Cancelling a cancelled job returns its current state. Succeeded and failed jobs return `409`.

The built-in types are `mock.echo` (optional `delay_ms`), `mock.sleep` (`duration_ms`, default 100), `mock.fail` (optional `delay_ms`, `error_message`, and `permanent`), and `mock.flaky` (`succeed_on_attempt`, default 2).

A worker renews its lease while an attempt runs. An expired lease lets another worker retry the job if attempts remain. A cancellation request takes precedence during recovery. Job handlers should honor context cancellation and make external side effects safe to repeat because a worker can lose its lease after performing work. The enqueue idempotency key deduplicates job creation.

To run the PostgreSQL lifecycle, concurrency, and API integration tests, point `TEST_DATABASE_URL` at a PostgreSQL database whose user can create schemas and run `make test-integration`.

### Scan a repository

Associate the repository with a project, then submit a scan through the jobs API.

```sh
curl -X POST http://localhost:8080/api/v1/jobs \
  -H 'Content-Type: application/json' \
  -d '{"type":"repository.scan","payload":{"project_id":"<project UUID>","repository_id":"<repository UUID>","ref":"main"}}'
```

Omit `ref` to scan `HEAD`. A ref can also be a branch, tag, or commit SHA. The job result contains commit metadata, a sorted manifest with Git blob IDs and SHA-256 hashes, skip reasons, and scan totals. It contains no file contents. Local scans use committed Git objects, including when the index or working tree has pending changes. Local repositories and their Git directories must be inside the worker's `REPO_ALLOWED_LOCAL_ROOTS`. With Docker Compose, paths and mounts must be accessible inside the container that runs the job.

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
