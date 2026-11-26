# ProjectLens Web Console API mapping

[OpenAPI](../api/openapi.yaml) defines the HTTP contract. Phase 1 renders the route shell. The tables describe the data and actions for the planned feature views.

In the API tables, **P** expands to `/api/v1/projects/{id}` and **R** expands to `/api/v1/projects/{id}/repositories/{repoId}`. The frontend route parameter `:projectId` supplies the project UUID used as `{id}`. Encode identifiers and query values for their location in the URL. Send canonical paths without a trailing slash.

## Global routes

| View and action | API | UI behavior |
| --- | --- | --- |
| `/`, List projects | `GET /api/v1/projects` | Page by `offset` and `limit`. Filter by `status=active` or `archived`. Use the reported `total`. |
| `/`, Create project | `POST /api/v1/projects` | Send `slug`, `name`, and optional `description` and `status`. Open the newly created project using its UUID. |
| `/`, Edit or archive project | `GET P`, `PATCH P` | Read current values and send the changed fields. Treat archive as a status change. A slug conflict returns 409. |
| `/`, Delete project | `DELETE P` | Confirm permanent deletion. Handle 204 with no body and refresh the list. |
| `/jobs`, List jobs | `GET /api/v1/jobs` | Filter by `type` or `status`; page by `offset`, `limit`, and `total`. Statuses are `pending`, `running`, `succeeded`, `failed`, and `cancelled`. |
| `/jobs`, Inspect and poll a job | `GET /api/v1/jobs/{id}` | Show attempt, timestamps, payload, result, and available error details. Poll active jobs and stop on a terminal state. |
| `/jobs`, Cancel a job | `POST /api/v1/jobs/{id}/cancel` | Offer on pending or running jobs. Render the returned state and continue polling while cancellation is pending. |
| `/settings`, Check liveness | `GET /healthz` | Show process status, service, environment, version, and uptime. |
| `/settings`, Check readiness | `GET /readyz` | Show PostgreSQL and Redis checks. PostgreSQL failure returns 503. Redis may be disabled or degraded while PostgreSQL still serves search. |
| `/settings`, Test API access | `GET /api/v1/ping` | Test the selected API origin and bearer token. This route passes through auth middleware when auth is enabled. |
| `/settings`, Inspect the contract | `GET /openapi.json`, `GET /openapi.yaml` | Open the public JSON contract or its YAML representation. |
| `/settings`, Configure the API base URL and token | Client-side settings | Apply the connection policy below. |

### Connection and browser policy

- Default to same-origin relative URLs. During development Vite proxies `/api`, `/healthz`, `/readyz`, `/openapi.json`, and `/openapi.yaml` to `VITE_API_PROXY_TARGET` or `http://localhost:8080`. A production same-origin deployment routes those paths to Go and serves the SPA index for frontend deep links.
- Persist only the nonsecret base URL in local storage. An explicit URL must be an HTTP(S) origin without embedded credentials, a path other than `/`, a query, or a fragment. Use HTTPS for remote deployments. Hold a user-entered token in memory for the current tab and clear it on reload or when the API origin changes. Exclude tokens from URLs, persisted caches, diagnostics, and build-time variables.
- Send the bearer token in `Authorization` only to the selected API origin on authenticated routes. Public diagnostics remain usable without it. On 401, prompt the user to check the connection settings.
- The default CORS allowed headers are `Authorization`, `Content-Type`, and `X-Request-ID`, with `If-Match` absent. The current environment loader supports `CORS_ALLOWED_ORIGINS` and the allowed headers are configured through YAML `cors.allowed_headers`. Keep versioned writes same-origin through the Vite proxy or production routing, or send the body `version` for cross-origin PUT/PATCH. A version-checked DELETE requires `If-Match`.
- The Go CORS middleware omits `Access-Control-Expose-Headers`. Cross-origin JavaScript can use body fields such as `version`, `search_version`, `cache_status`, and `poll_url`. Same-origin routing makes the HTTP metadata `ETag`, `X-Request-ID`, `Location`, `X-Search-Cache`, and `X-Search-Version` available to the UI.

## Project workspace

`/projects/:projectId` redirects to `/projects/:projectId/overview` in the client.

### Overview

| Section | API | UI behavior |
| --- | --- | --- |
| Project summary | `GET P` | Use the current project name, slug, status, and description. |
| Repository and document summaries | `GET P/repositories`, `GET P/documents` | Read a bounded page of each resource. Use the reported totals for counts. |
| Sync policies | `GET R/sync-policy` for each displayed repository | Aggregate configured policies for the displayed repositories. The API has no project-wide `GET P/sync-policy`. |
| Recent project jobs | `GET /api/v1/jobs` | Filter by `payload.project_id` in the browser. The jobs API has no project filter, so the server's `total` counts global results. Continue through global pages as needed for the recent slice. |

### Repositories

The list is at `/projects/:projectId/repositories`. Repository detail and its sections use `/projects/:projectId/repositories/:repoId/*`.

| View and action | API | UI behavior |
| --- | --- | --- |
| List and filter | `GET P/repositories` | Use `source_type=local_git\|github`, `role=primary\|reference`, `limit`, and `offset`. |
| Associate | `POST P/repositories` | Accept a local Git path on the server or a public GitHub owner and repository. Local sources must fall within the configured allowlist. Use `RepositoryInput` for the selected source. |
| Read or edit association | `GET R`, `PATCH R` | Patch name, description, or role. |
| Disassociate | `DELETE R` | Confirm removal of the repository's snapshots, indexed files, history, GitHub activity, and policies. Active jobs targeting the repository receive cancellation requests. Repository documents survive as project documents with a new version and read-only `origin`. |
| Publish searchable files | `POST R/sync` | Send an optional JSON `ref`. Poll the receipt's `poll_url`. Refresh the active snapshot and search results after successful publication. |
| Import commits | `POST R/commits/sync` | Send an optional JSON `ref` and poll the returned job. Each call adds up to 100 commits to the import window, capped at 2000 per ref. |
| Import GitHub activity | `POST R/github-activity/sync` | For GitHub repositories, enqueue a job and read its outcome from the jobs API. The import is subject to upstream availability and rate limits. |
| Refresh repository metadata | `POST R/refresh-metadata` | Update the association's metadata and replace the displayed repository record from the result. Publishing files uses the separate sync action. |
| View and update schedules | `GET R/sync-policy`, `PUT R/sync-policy/{kind}` | Read the policy list, then select `files`, `commits`, or `github_activity` from it. Send `enabled` and `interval_seconds` to update one kind. GitHub activity requires a GitHub source. The default minimum interval is 60 seconds and is configurable. The API has no individual policy GET. |
| Browse snapshots | `GET R/snapshots`, `GET R/snapshots/active` | Show published versions, scan counts, and the active snapshot. If no active snapshot exists, offer to publish one. |
| Browse active files | `GET R/files`, `GET R/files/content?path={path}` | Filter indexed files by `q` or `path_prefix`. Read indexed content using the exact relative path. |
| Browse branches and tags | `GET R/refs` | Filter via optional `type=branch\|tag` and page by offset and limit. |
| Browse imported commits | `GET R/commits`, `GET R/commits/{sha}` | Select a ref for the list, and pass a full SHA for detail. Communicate the imported history window. |
| Read changed files | `GET R/commits/{sha}/files` | Page through changes against the first parent. |
| Browse a historical tree or file | `GET R/commits/{sha}/tree`, `GET R/commits/{sha}/file?path={path}` | Browse the tree with optional `path_prefix`. Read a regular file at the selected full commit SHA. |
| Compare commits | `GET R/compare?base={base}&head={head}` | Compare imported commits and indicate bounded or truncated patch output. |
| Read file history | `GET R/files/history?path={path}` | Optionally supply `ref` and `follow=true` to follow renames. |
| Read blame | `GET R/files/blame?path={path}` | Optionally supply `ref` and both inclusive 1-based line bounds for a range of up to 2000 lines. Show truncation when the default range is capped. |
| Search or manage repository documents | `GET R/search` and the repository document routes below | Keep actions scoped to the repository selected in the frontend URL. |

The three sync actions return `job_id`, `status`, and `poll_url`. HTTP 202 represents a new job and 200 represents reuse of an active job. A different active ref can produce a conflict. Honor per-endpoint pagination metadata and keep history truncation distinct from page navigation.

### Search

At `/projects/:projectId/search`, use `GET P/search` to search indexed active snapshots and Markdown documents. Require a nonblank `q` and send an explicit `scope` for every project search. The server can infer a scope from `repository_id`, `repository_ids`, or `repository_role` and returns 400 when the request supplies neither scope nor inference filters.

| Scope | Content and filters |
| --- | --- |
| `project` | Project-level documents. Omit all repository filters. |
| `repository` | One repository's active files and owned documents. Require `repository_id` and omit `repository_ids` and `repository_role`. |
| `cross_repository` | Repository files and owned documents, optionally narrowed by `repository_ids` and/or `repository_role`. Omit singular `repository_id`. Offer `include_project_documents=true` in this scope only. |
| `all` | Project-level documents and all repository content. Omit all repository filters. |

Strip incompatible filters when scope changes. The handler rejects `include_project_documents` even when explicitly false outside `cross_repository`. For `GET R/search`, the path selects one repository. Omit redundant scope filters, or supply `scope=repository` and a matching `repository_id`. The route rejects filters that widen its scope.

Support `source_type` (`local_git`, `github`, `document`), `path_prefix`, `limit`, and `offset`. Both `source_type` and `repository_ids` accept repeated values or comma-separated lists. The search service trims the query and defaults to a 200-character maximum with 20 results per page and a configurable limit cap of 100. Use the response `total` for pagination. Read `X-Search-Cache` for `hit`, `miss`, `bypass`, or `degraded`, and `X-Search-Version` for the search generation. The body also carries `cache_status` and `search_version`.

The backend escapes snippet text and inserts literal `<mark>` and `</mark>` delimiters. Recognize only balanced pairs of those exact tags before decoding text entities. Render the decoded text through React text nodes and create React `<mark>` elements for highlighted runs. Render malformed snippets as text and avoid injecting HTML. Use `kind` and typed `location` fields to link to the corresponding file, document, or line. The supplied `location.api_path` is an API path for data access.

### Documents

`/projects/:projectId/documents` lists summaries and `/projects/:projectId/documents/:docId` reads or edits Markdown. Repository-owned views can also appear under the repository route.

| Action | Project path | Repository path |
| --- | --- | --- |
| List summaries | `GET P/documents` | `GET R/documents` |
| Create Markdown document | `POST P/documents` | `POST R/documents` |
| Read content | `GET P/documents/{docId}` | `GET R/documents/{docId}` |
| Replace content | `PUT P/documents/{docId}` | `PUT R/documents/{docId}` |
| Edit selected fields | `PATCH P/documents/{docId}` | `PATCH R/documents/{docId}` |
| Delete | `DELETE P/documents/{docId}` | `DELETE R/documents/{docId}` |

- The project collection includes both ownership scopes by default. Filter by `scope=project` or `scope=repository` and use `repository_id` only with repository ownership. Repository lists enforce the path repository and accept only matching ownership filters. Use `total`, `limit`, and `offset` from the response.
- Create project documents without `repository_id` or with a null value. Create repository documents from the scoped route, where a supplied body `repository_id` must match the path. Use the API's title and content fields. The configured default content limit is 1 MiB in bytes. Render Markdown with raw HTML disabled.
- Document reads and successful create/update responses carry the current numeric body `version` and an `ETag` that quotes it. PUT and PATCH require a positive integer body `version` or a single `If-Match` version. When both are supplied they must agree. The handler accepts a quoted number, weak numeric ETag, or unquoted number. DELETE accepts optional `If-Match` and returns 204 on success.
- On `409 version_conflict`, preserve the local draft, fetch the latest document, and show the current and draft versions. Let the user reload or reconcile the changes and explicitly retry against the latest version. For a delete conflict, refresh and require confirmation of the current version.
- When disassociation moves a repository document into project scope, use the project-level route. Show `origin.repository_name` as provenance. The server owns `origin`, rejects it in writes, and uses current ownership for scoping.

### Activity

At `/projects/:projectId/activity`, let users switch among commits, issues, and pull requests. Repository drill-downs use the same reads within the repository workspace.

| View | API |
| --- | --- |
| Commits | `GET R/commits` for each selected repository. A combined project view merges per-repository results in the client. |
| Issue lists | `GET P/issues`, `GET R/issues` |
| Issue detail and comments | `GET R/issues/{number}`, `GET R/issues/{number}/comments` |
| Pull request lists | `GET P/pulls`, `GET R/pulls` |
| Pull request detail, comments, and commits | `GET R/pulls/{number}`, `GET R/pulls/{number}/comments`, `GET R/pulls/{number}/commits` |

Project-level issue and pull lists return the owning `repository_id`. Pair that UUID with the issue or pull request number for detail. Imported pull request comments can include review comments. `ActivityPage` contains `items`, `limit`, and `offset` without `total` or `has_more`. A short page establishes the end. After a full page, allow a probe for the next page and handle an empty result without displaying a guessed total.

## Handler details and extension points

- The strict query parser in [HistoryHandler](../internal/handler/history.go) covers refs, historical trees and files, file history, and blame. It rejects unknown or repeated query keys and empty required paths or typed values such as `limit`, `offset`, `follow`, and line bounds. Omit blank optional fields. Some optional strings such as `type` allow an empty value. The commit list/detail/changes and compare routes use a different parser, so serialize each operation's documented fields.
- Repository creation and metadata refresh depend on server-side allowlists and GitHub configuration. Client API settings configure ProjectLens access. GitHub tokens and local repository roots are server configuration.
- Map structured `error` and `code` fields plus `X-Request-ID` when accessible to the error banner. Preserve inputs when a request fails. Offer targeted recovery for invalid input, stale document versions, upstream rate limits, and server availability. Treat 204 responses as empty. Sources include the [handlers](../internal/handler/), [configuration defaults](../internal/config/config.go), and [CORS middleware](../internal/middleware/cors.go).
- Future data access can use domain-specific TanStack Query hooks inside the route tree with keys that include backend origin, project or repository ID, and filters. The current API has no AI chat, RAG, or multi-agent endpoints. Future capabilities can add a route module and typed query and mutation hooks once their operations are defined in OpenAPI.
