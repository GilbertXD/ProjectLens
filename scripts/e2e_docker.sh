#!/usr/bin/env bash
set +x
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
for tool in docker curl jq openssl; do
  command -v "$tool" >/dev/null || { echo "Missing required command $tool" >&2; exit 1; }
done

# Preserve the host's Go module proxy for the image build.
host_goproxy="${GOPROXY:-}"
if [[ -z "$host_goproxy" ]] && command -v go >/dev/null; then
  host_goproxy="$(go env GOPROXY)"
fi

project="projectlens-e2e-$$"
image="$project:fixture"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/projectlens-e2e.XXXXXXXX")"
scratch="$(cd "$scratch" && pwd -P)"
token="$(openssl rand -hex 32)"

# Ignore app settings inherited from a developer shell and local .env files.
while IFS= read -r key; do
  unset "$key"
done < <(grep -oE '\$\{[A-Z_][A-Z_0-9]*' docker-compose.yml | cut -c3- | sort -u)
unset COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_PROJECT_NAME COMPOSE_FILE
cat > "$scratch/env" <<ENV
APP_ENV=test
WORKER_ENABLE_MOCK_JOBS=true
API_PORT=127.0.0.1:0
POSTGRES_PORT=127.0.0.1:0
REDIS_PORT=127.0.0.1:0
AUTH_ENABLED=true
AUTH_BEARER_TOKENS=$token
GITHUB_API_BASE_URL=http://github-fixture:8000
GITHUB_GIT_BASE_URL=http://github-fixture:8000
REPO_ALLOWED_LOCAL_ROOTS=/fixtures/local
SERVER_MAX_BODY_BYTES=32768
WORKER_POLL_INTERVAL=100ms
WORKER_RETRY_BASE_DELAY=100ms
E2E_IMAGE=$image
ENV
if [[ -n "$host_goproxy" ]]; then
  printf 'GOPROXY=%s\n' "$host_goproxy" >> "$scratch/env"
fi
chmod 600 "$scratch/env"
export E2E_FIXTURE_DIR="$scratch/fixtures" E2E_FIXTURE_BUILD_DIR="$scratch/fixture-build" E2E_IMAGE="$image"
mkdir -p "$E2E_FIXTURE_DIR" "$E2E_FIXTURE_BUILD_DIR"
cat > "$E2E_FIXTURE_BUILD_DIR/Dockerfile" <<'DOCKERFILE'
FROM golang:1.25-alpine AS build
COPY main.go /tmp/main.go
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /fixture /tmp/main.go

FROM alpine:3.21
COPY --from=build /fixture /fixture
USER 65534:65534
ENTRYPOINT ["/fixture"]
DOCKERFILE
cat > "$E2E_FIXTURE_BUILD_DIR/main.go" <<'GO'
package main

import (
    "log"
    "net/http"
    "time"
)

func main() {
    server := &http.Server{
        Addr: ":8000",
        Handler: http.FileServer(http.Dir("/fixtures/web")),
        ReadHeaderTimeout: 5 * time.Second,
    }
    log.Fatal(server.ListenAndServe())
}
GO
# Only the fixture generator has a writable mount. The enclosing directory is private.
chmod 0777 "$E2E_FIXTURE_DIR"
cat > "$scratch/compose.yml" <<'YAML'
services:
  api:
    image: "${E2E_IMAGE}"
    volumes:
      - "${E2E_FIXTURE_DIR}/local:/fixtures/local:ro"
    depends_on:
      github-fixture:
        condition: service_healthy
  github-fixture:
    build: "${E2E_FIXTURE_BUILD_DIR}"
    volumes:
      - "${E2E_FIXTURE_DIR}/web:/fixtures/web:ro"
    healthcheck:
      test: ["CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8000/repos/owner/project"]
      interval: 1s
      timeout: 2s
      retries: 15
  fixture-init:
    image: "${E2E_IMAGE}"
    profiles: ["fixture-init"]
    volumes:
      - "${E2E_FIXTURE_DIR}:/fixtures"
    healthcheck:
      disable: true
YAML

compose() {
  docker compose --project-name "$project" --env-file "$scratch/env" \
    -f docker-compose.yml -f "$scratch/compose.yml" "$@"
}

refresh_api_url() {
  local api_address
  api_address="$(compose port api 8080)"
  api="http://127.0.0.1:${api_address##*:}"
}

cleanup() {
  local status=$?
  trap - EXIT
  if (( status != 0 )); then
    compose ps -a >&2 || true
    # Capture service diagnostics without emitting token material.
    if compose logs --no-color --tail=80 api > "$scratch/failure.log" 2>/dev/null; then
      sed "s/$token/[REDACTED]/g" "$scratch/failure.log" >&2
    fi
  fi
  compose down -v --remove-orphans --rmi local || status=1
  if docker image inspect "$image" >/dev/null 2>&1; then
    docker image rm "$image" >/dev/null || status=1
  fi
  rm -rf "$scratch" || status=1
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

compose config -q
compose build api github-fixture
compose run --rm --no-deps --entrypoint /bin/sh fixture-init -ec '
  umask 022
  mkdir -p /fixtures/local/worktree /fixtures/remote /fixtures/web/repos/owner /fixtures/web/owner
  git_fixture() {
    repo=$1
    filename=$2
    git -C "$repo" init --initial-branch=main --template=
    printf "deliveryquasar indexed fixture\n" > "$repo/$filename"
    git -C "$repo" add -- "$filename"
    git -C "$repo" -c user.name=Fixture -c user.email=fixture@example.org commit -m "Git fixture"
  }
  git_fixture /fixtures/local/worktree local.txt
  git_fixture /fixtures/remote remote.txt
  git clone --bare --no-hardlinks /fixtures/remote /fixtures/web/owner/project.git
  git --git-dir=/fixtures/web/owner/project.git update-server-info
  printf "%s\n" '"'"'{"id":42,"full_name":"owner/project","visibility":"public","default_branch":"main","html_url":"https://github.com/owner/project"}'"'"' \
    > /fixtures/web/repos/owner/project
  # Allow the host to remove fixture directories after the app-owned bind mounts stop.
  find /fixtures -mindepth 1 -type d -exec chmod a+rwx {} +
'
compose up -d --no-build api
refresh_api_url
headers="$scratch/headers"
body="$scratch/body"
printf 'Authorization: Bearer %s\n' "$token" > "$scratch/auth"
chmod 600 "$scratch/auth"

request() {
  local expected="$1" method="$2" path="$3" auth="$4" data="${5-}" status
  local args=(-sS --max-time 15 -X "$method" -D "$headers" -o "$body" -w '%{http_code}')
  if [[ "$auth" == yes ]]; then args+=(-H "@$scratch/auth"); fi
  if [[ -n "$data" ]]; then args+=(-H 'Content-Type: application/json' --data-binary "$data"); fi
  status="$(curl "${args[@]}" "$api$path")"
  if [[ "$status" != "$expected" ]]; then
    printf '%s %s returned %s, expected %s\n' "$method" "$path" "$status" "$expected" >&2
    sed "s/$token/[REDACTED]/g" "$body" >&2
    exit 1
  fi
}

wait_ready() {
  local redis="$1" attempt
  for ((attempt = 0; attempt < 90; attempt++)); do
    if curl -fsS --max-time 2 "$api/healthz" >/dev/null 2>&1 &&
       curl -fsS --max-time 2 "$api/readyz" 2>/dev/null |
         jq -e --arg redis "$redis" '.status == "ok" and .checks.postgres == "ok" and .checks.redis == $redis' >/dev/null; then
      return
    fi
    sleep 1
  done
  echo "Timed out waiting for readiness with Redis $redis" >&2
  exit 1
}

wait_ready ok
request 200 GET /openapi.yaml no
grep -Eq '^openapi: "?3\.0\.3"?$' "$body"
request 200 GET /openapi.json no
jq -e '.openapi == "3.0.3" and .components.securitySchemes.BearerAuth.scheme == "bearer"' "$body" >/dev/null
request 401 GET /api/v1/ping no
grep -iq '^WWW-Authenticate: Bearer realm="ProjectLens"' "$headers"
jq -e '.code == "unauthorized" and (.request_id | length > 0)' "$body" >/dev/null
request 200 GET /api/v1/ping yes

request 201 POST /api/v1/projects yes '{"slug":"delivery","name":"Delivery fixture"}'
project_id="$(jq -er '.id' "$body")"
project_api="/api/v1/projects/$project_id"
request 403 POST "$project_api/repositories" yes '{"source_type":"local_git","source":"/app"}'
jq -e '.code == "forbidden"' "$body" >/dev/null
jq -n '{title:"Oversized",content:("x" * 33000)}' > "$scratch/oversized.json"
request 413 POST "$project_api/documents" yes "@$scratch/oversized.json"
jq -e '.code == "payload_too_large"' "$body" >/dev/null

enqueue_and_wait() {
  local job status attempt
  request 202 POST /api/v1/jobs yes "$1"
  job="$(jq -er '.id' "$body")"
  for ((attempt = 0; attempt < 120; attempt++)); do
    request 200 GET "/api/v1/jobs/$job" yes
    status="$(jq -er '.status' "$body")"
    case "$status" in
      succeeded) return ;;
      failed|cancelled)
        echo "Job $job ended with status $status" >&2
        jq -r '.last_error_code // empty' "$body" >&2
        return 1 ;;
    esac
    sleep 0.5
  done
  echo "Timed out waiting for job $job" >&2
  return 1
}

import_and_sync() {
  local kind="$1" source="$2" repo commit
  request 201 POST "$project_api/repositories" yes \
    "$(jq -nc --arg kind "$kind" --arg source "$source" '{source_type:$kind,source:$source}')"
  repo="$(jq -er '.id' "$body")"
  enqueue_and_wait "$(jq -nc --arg project "$project_id" --arg repo "$repo" \
    '{type:"repository.sync",payload:{project_id:$project,repository_id:$repo,ref:"main"}}')"
  commit="$(jq -er '.result | select(.status == "published") | .commit_hash' "$body")"
  request 200 GET "$project_api/search?q=deliveryquasar&source_type=$kind" yes
  jq -e --arg kind "$kind" --arg repo "$repo" --arg commit "$commit" \
    '.total == 1 and (.items[0] | .source_type == $kind and .location.repository_id == $repo and .location.commit_hash == $commit)' "$body" >/dev/null
}
import_and_sync local_git /fixtures/local/worktree
import_and_sync github https://github.com/owner/project

search_cache() {
  local expected="$1" hits="$2"
  request 200 GET "$project_api/search?q=deliveryquasar" yes
  tr -d '\r' < "$headers" | grep -ix "X-Search-Cache: $expected" >/dev/null
  grep -iq '^X-Search-Version: [1-9][0-9]*' "$headers"
  jq -e --arg expected "$expected" --argjson hits "$hits" \
    '.cache_status == $expected and .total == $hits' "$body" >/dev/null
}

search_cache miss 2
search_cache hit 2
old_version="$(jq -er '.search_version' "$body")"
request 201 POST "$project_api/documents" yes '{"title":"Guide","content":"# Delivery\nDeliveryquasar in Markdown"}'
doc_id="$(jq -er '.id' "$body")"
search_cache miss 3
jq -e --arg id "$doc_id" --argjson old "$old_version" \
  '([.items[].source_type] | sort) == ["document","github","local_git"]
   and any(.items[]; .source_type == "document" and .location.document_id == $id)
   and .search_version > $old' "$body" >/dev/null
search_cache hit 3

compose stop redis
wait_ready degraded
search_cache degraded 3
request 200 PATCH "$project_api/documents/$doc_id" yes '{"version":1,"content":"# Revised\nDeliveryquasar after Redis outage"}'
search_cache degraded 3
compose start redis
wait_ready ok
search_cache miss 3
search_cache hit 3

enqueue_and_wait '{"type":"mock.flaky","max_attempts":3,"payload":{"succeed_on_attempt":2}}'
jq -e '.attempt == 2 and .result.recovered == true' "$body" >/dev/null
compose restart api
refresh_api_url
wait_ready ok
request 200 GET "$project_api" yes
jq -e --arg id "$project_id" '.id == $id and .slug == "delivery"' "$body" >/dev/null
search_cache hit 3

compose logs --no-color > "$scratch/logs"
if grep -Fq "$token" "$scratch/logs"; then
  echo "Bearer token leaked in container logs" >&2
  exit 1
fi
echo "Docker delivery E2E passed"
