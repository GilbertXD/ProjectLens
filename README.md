# ProjectLens

ProjectLens is a Go 1.25 HTTP service built with Gin.

## Run locally

Start the server, then run the `curl` commands from another terminal.

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
