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

The server applies embedded migrations on startup by default. Set `DB_AUTO_MIGRATE=false` if you apply migrations separately using `make migrate-up`. Use `make migrate-status` to inspect migration state. `make migrate-down` rolls back the latest migration and currently drops the project and audit tables.

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
