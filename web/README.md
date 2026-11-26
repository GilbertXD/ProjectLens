# ProjectLens — See beyond the code.

The ProjectLens web console uses React, TypeScript, and Vite. React Router defines two navigation levels, TanStack Query loads and updates live Go API data, Tailwind CSS supports styling, and CodeMirror 6 displays indexed source and edits Markdown.

- **Global** covers Projects (`/`), Jobs (`/jobs`), and Settings (`/settings`). Create or open a project, monitor background jobs, and configure or diagnose the API connection.
- **Project workspace** lives under `/projects/:projectId` and includes Overview, Repositories, Search, Documents, and Activity. Associate a repository and choose **Sync files** to index a snapshot. Open the repository to browse files, commits, and snapshots. In Search, choose which project documents or repositories to include. Use Documents to manage Markdown notes and Activity to review repository discussions.

## Prerequisites

- Use Node.js 24.15+ on the 24.x line for development and verification. Vite supports Node.js 20.19+ on the 20.x line. The locked jsdom version used by the test suite requires Node.js 22.22.2+ on 22.x, 24.15+ on 24.x, or 26+.
- Start the Go backend from the repository root with `make run` after starting PostgreSQL and Redis, or use `docker compose up --build -d` to start the backend and its dependencies together. See the [root README](../README.md) for backend setup and credentials.

## Start the console

From the repository root, install the locked dependencies and start Vite.

```sh
cd web && npm ci && npm run dev
```

Open the URL printed by Vite, usually `http://localhost:5173`. Leave **API base URL** blank in Settings to use same-origin requests. Vite proxies `/api`, `/healthz`, `/readyz`, `/openapi.json`, and `/openapi.yaml` to `http://localhost:8080` by default. Set `VITE_API_PROXY_TARGET` in the environment when starting Vite to use a different backend.

```sh
VITE_API_PROXY_TARGET=http://localhost:8081 npm run dev
```

## Connect to the API

If backend authentication is enabled, open **Settings** at `/settings`, enter the backend's Bearer token, and select **Set token**. Select **Test connection** to check authenticated access. The Health and Readiness panels and OpenAPI links use public endpoints.

The token lives in memory for the current tab. The console sends it as an `Authorization: Bearer <token>` header on authenticated API requests to the selected backend. The token is never hardcoded or persisted to `localStorage`. Reloading the tab, changing the API base URL, or selecting **Clear token** clears it. The API base URL itself can persist in browser storage. Select **Reset API base URL** to return to same-origin requests.

To use a separate API origin, enter an HTTP or HTTPS origin in **API base URL** and save it before setting the token. Configure the backend's `CORS_ALLOWED_ORIGINS` with the exact console origin, including its port if present. Allow the API methods and request headers the console uses, including `Authorization` and `Content-Type`. For version-checked document writes that send `If-Match`, also allow that header via `cors.allowed_headers` in the backend YAML. Browsers restrict access to response headers such as `ETag` and `X-Request-ID` across origins unless the serving layer exposes them. Go's default CORS middleware leaves these headers unexposed. Use same-origin routing to make the headers available to the console.

## Verify and build

Run these commands from `web/`.

| Command | Purpose |
| --- | --- |
| `npm run typecheck` | Check TypeScript types. |
| `npm run test` | Run the Vitest suite in jsdom. |
| `npm run build` | Build production assets into `dist/`. |
| `npm run preview` | Serve the production build locally for a smoke test. |

For a same-origin production deployment, serve `dist/`, route the proxied API and diagnostic paths to Go, and serve `index.html` for frontend deep links such as `/projects/:projectId/overview`.

See the [web design system](../docs/WEB_DESIGN_SYSTEM.md) for layout and component guidance and the [web API mapping](../docs/WEB_API_MAPPING.md) for route, request, and response details.
