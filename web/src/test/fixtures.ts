import type {
  BlameLine, CommitComparison, CommitHistoryEntry, CommitHistoryPage, Document, FileChange,
  FileHistoryEntry, GitHubActivity, GitRef, Health, Job, JobStatus, Project, Readiness,
  Repository, Snapshot, SourceFile, SyncPolicy,
} from '@/types/domain'

export const fixtureIds = {
  core: '10000000-0000-4000-8000-000000000001',
  bench: '10000000-0000-4000-8000-000000000002',
  legacy: '10000000-0000-4000-8000-000000000003',
  api: '20000000-0000-4000-8000-000000000001',
  gin: '20000000-0000-4000-8000-000000000002',
  tokens: '20000000-0000-4000-8000-000000000003',
  retired: '20000000-0000-4000-8000-000000000004',
  handbook: '30000000-0000-4000-8000-000000000001',
  routes: '30000000-0000-4000-8000-000000000002',
  migration: '30000000-0000-4000-8000-000000000003',
}

const updated = '2026-11-24T10:42:00Z'
const previous = '2026-11-20T08:16:00Z'
const timestamps = { created_at: previous, updated_at: updated }

export const projects: Project[] = [
  { ...timestamps, id: fixtureIds.core, name: 'ProjectLens Core', slug: 'projectlens-core', status: 'active',
    description: 'Repository indexing, search, and operational tooling for the ProjectLens console.' },
  { ...timestamps, id: fixtureIds.bench, name: 'Query Bench', slug: 'query-bench', status: 'active',
    description: 'Relevance experiments and latency benchmarks for scoped search.' },
  { ...timestamps, id: fixtureIds.legacy, name: 'Legacy Indexer', slug: 'legacy-indexer', status: 'archived',
    description: 'Historical reference for the first-generation indexing pipeline.', updated_at: previous },
]

const repositoryBase = { ...timestamps, project_id: fixtureIds.core, last_synced_at: updated }
const repositories: Repository[] = [
  { ...repositoryBase, id: fixtureIds.api, source_type: 'local_git', role: 'primary', name: 'projectlens-api',
    source: '/work/projectlens-api', canonical_source: '/work/projectlens-api', local_path: '/work/projectlens-api',
    default_branch: 'main', description: 'Go HTTP service and repository indexing pipeline.' },
  { ...repositoryBase, id: fixtureIds.gin, source_type: 'github', role: 'reference', name: 'gin-gonic/gin',
    source: 'https://github.com/gin-gonic/gin', canonical_source: 'https://github.com/gin-gonic/gin',
    remote_url: 'https://github.com/gin-gonic/gin', github_owner: 'gin-gonic', github_repo: 'gin',
    github_visibility: 'public', github_language: 'Go', default_branch: 'master',
    description: 'Upstream HTTP routing and middleware reference.' },
  { ...repositoryBase, id: fixtureIds.tokens, source_type: 'local_git', role: 'reference', name: 'design-tokens',
    source: '/work/design-tokens', canonical_source: '/work/design-tokens', local_path: '/work/design-tokens',
    default_branch: 'main', description: 'Shared typography, spacing, and console surface tokens.' },
]

const identity = {
  author_name: 'Mira Chen', author_email: 'mira@example.test',
  committer_name: 'Mira Chen', committer_email: 'mira@example.test',
}

function commit(hash: string, subject: string, time: string, parents: string[] = []): CommitHistoryEntry {
  return { ...identity, hash, tree_hash: hash.slice(1) + 'a', subject, message: subject,
    authored_at: time, committed_at: time, parents, truncated: false }
}

const base = commit('14caccc26456e460d5ad31eb75b8ec3d546df049', 'Register the project search route', previous)
const head = commit('8ef6afd5c452682c04a2bd19e47622dfed8c7638', 'Require an explicit search scope', updated, [base.hash])
const ginHead = commit('edf2187c5635a265aec67a14b16739e374a367c7', 'Document route group middleware', updated)
const tokensHead = commit('9ec8e4eaa0db990d98d6bc112f068cc853637f20', 'Align table and rail tokens', updated)

export function byteLength(value: string) {
  return new TextEncoder().encode(value).length
}

function snapshot(repo: Repository, entry: CommitHistoryEntry | undefined, version: number,
  status: Snapshot['status'], files: SourceFile[], time: string, id: string = crypto.randomUUID()): Snapshot {
  return {
    id, repository_id: repo.id, version, status, resolved_ref: repo.default_branch,
    commit_hash: entry?.hash ?? '', tree_hash: entry?.tree_hash ?? '', scanner_config_hash: 'b5c2f02a',
    commit_author_name: entry?.author_name ?? '', commit_author_email: entry?.author_email ?? '',
    commit_authored_at: entry?.authored_at ?? time, commit_committer_name: entry?.committer_name ?? '',
    commit_committer_email: entry?.committer_email ?? '', commit_committed_at: entry?.committed_at ?? time,
    commit_subject: entry?.subject ?? '', commit_parents: entry?.parents ?? [],
    total_tree_entries: files.length, included_files: files.length,
    included_bytes: files.reduce((sum, file) => sum + file.size_bytes, 0),
    skipped_files: 0, skipped_by_reason: {}, truncated: false,
    added_files: version === 1 ? files.length : 0, modified_files: 0, deleted_files: 0,
    unchanged_files: version === 1 ? 0 : files.length, blobs_read: files.length,
    scanned_at: time, published_at: time, created_at: time, updated_at: time,
  }
}

export const searchPath = 'internal/search/scope.go'
const searchSource = `package search

import "errors"

type Scope string

func ValidateScope(scope Scope) error {
	switch scope {
	case "project", "repository", "cross_repository", "all":
		return nil
	default:
		return errors.New("explicit search scope required")
	}
}`

const sources: Record<string, Record<string, string>> = {
  [fixtureIds.api]: {
    'cmd/projectlens/main.go': `package main

import "log"

func main() {
	log.Print("starting ProjectLens search service")
}`,
    [searchPath]: searchSource,
    'internal/search/scope_test.go': `package search

import "testing"

func TestValidateScope(t *testing.T) {
	if ValidateScope("") == nil {
		t.Fatal("empty search scope accepted")
	}
}`,
  },
  [fixtureIds.gin]: {
    'routergroup.go': `package gin

// RouterGroup prefixes routes and groups middleware.
type RouterGroup struct {
	basePath string
	handlers []HandlerFunc
}

func (group *RouterGroup) BasePath() string {
	return group.basePath
}`,
  },
  [fixtureIds.tokens]: {
    'src/tokens/layout.ts': `export const workspace = {
  railWidth: 228,
  maxHomeWidth: 1120,
  ruleWidth: 1,
} as const`,
  },
}

const changes: FileChange[] = [
  { path: searchPath, type: 'modified', binary: false, insertions: 1, deletions: 1 },
  { path: 'internal/search/scope_test.go', type: 'added', binary: false, insertions: 9, deletions: 0 },
]

const comparisons: CommitComparison[] = [{
  base: base.hash, head: head.hash, items: changes, total: changes.length, truncated: false, patch_truncated: false,
  patch: `diff --git a/internal/search/scope.go b/internal/search/scope.go
--- a/internal/search/scope.go
+++ b/internal/search/scope.go
@@ -7,6 +7,6 @@ type Scope string
 func ValidateScope(scope Scope) error {
 	switch scope {
-	case "project", "repository", "cross_repository", "all", "":
+	case "project", "repository", "cross_repository", "all":
 		return nil
 	default:
 		return errors.New("explicit search scope required")
diff --git a/internal/search/scope_test.go b/internal/search/scope_test.go
new file mode 100644
--- /dev/null
+++ b/internal/search/scope_test.go
@@ -0,0 +1,9 @@
+${sources[fixtureIds.api]['internal/search/scope_test.go'].split('\n').join('\n+')}`,
}]

interface ActivityThread {
  item: GitHubActivity
  comments: GitHubActivity[]
  commits: CommitHistoryEntry[]
}

const activity: ActivityThread[] = [
  { item: { ...timestamps, repository_id: fixtureIds.gin, kind: 'issue', number: 4182,
    title: 'Clarify middleware order for nested groups', state: 'open', user: { login: 'route-reader' },
    body: 'The routing guide needs an example showing inherited middleware on a nested route group.' },
  comments: [{ ...timestamps, repository_id: fixtureIds.gin, kind: 'issue_comment', id: 8401,
    user: { login: 'mira-c' }, body: 'A three-level example would help document the execution order.' }], commits: [] },
  { item: { ...timestamps, repository_id: fixtureIds.gin, kind: 'issue', number: 4180,
    title: 'Document the trailing slash redirect', state: 'closed', user: { login: 'max-routes' },
    body: 'Add a note explaining how the redirect preserves the query string.' }, comments: [], commits: [] },
  { item: { ...timestamps, repository_id: fixtureIds.gin, kind: 'pull_request', number: 4186,
    title: 'Add a nested middleware example', state: 'open', user: { login: 'mira-c' },
    body: 'Adds an example that registers route middleware before a group handler.' },
  comments: [{ ...timestamps, repository_id: fixtureIds.gin, kind: 'review_comment', id: 8402,
    user: { login: 'route-reader' }, body: 'Please include the expected order of the log entries.' }], commits: [ginHead] },
]

export function defaultSyncPolicy(kind: SyncPolicy['kind']): SyncPolicy {
  return { kind, enabled: false, interval_seconds: 3600 }
}

export interface RepositoryPreview {
  repository: Repository
  snapshots: Snapshot[]
  files: SourceFile[]
  refs: GitRef[]
  history?: CommitHistoryPage
  changes: Record<string, FileChange[]>
  comparisons: CommitComparison[]
  fileHistory: Record<string, FileHistoryEntry[]>
  blame: Record<string, BlameLine[]>
  activity: ActivityThread[]
  policies: SyncPolicy[]
}

export function emptyRepository(repository: Repository): RepositoryPreview {
  return { repository, snapshots: [], files: [], refs: [], changes: {}, comparisons: [],
    fileHistory: {}, blame: {}, activity: [], policies: [] }
}

const commitsByRepository = {
  [fixtureIds.api]: [head, base], [fixtureIds.gin]: [ginHead], [fixtureIds.tokens]: [tokensHead],
}

export const repositoryPreviews = repositories.map((repo, index): RepositoryPreview => {
  const items = commitsByRepository[repo.id as keyof typeof commitsByRepository]
  const files = Object.entries(sources[repo.id]).map(([path, content]): SourceFile => ({
    id: crypto.randomUUID(), snapshot_id: '', repository_id: repo.id, path, content,
    blob_oid: items[0].hash, mode: '100644', size_bytes: byteLength(content), sha256: 'a1'.repeat(32),
    line_count: content.split('\n').length, is_executable: false, created_at: updated,
  }))
  const active = snapshot(repo, items[0], 2, 'active', files, updated)
  const superseded = snapshot(repo, items.at(-1), 1, 'superseded', files, previous)
  active.parent_snapshot_id = superseded.id
  superseded.superseded_at = updated
  if (repo.id === fixtureIds.api) {
    Object.assign(active, { total_tree_entries: 4, skipped_files: 1, skipped_by_reason: { ignored: 1 },
      added_files: 1, modified_files: 1, unchanged_files: 1 })
    const removedFile = files.find((file) => file.path.endsWith('_test.go'))!
    superseded.total_tree_entries--
    superseded.included_files--
    superseded.included_bytes -= removedFile.size_bytes
    superseded.added_files--
    superseded.blobs_read--
  }
  const window_status = (['complete', 'partial', 'window_limit_reached'] as const)[index]
  return {
    ...emptyRepository(repo), snapshots: [active, superseded],
    files: files.map((file) => ({ ...file, snapshot_id: active.id })),
    refs: [{ type: 'branch', name: repo.default_branch, ref: 'refs/heads/' + repo.default_branch, object_hash: items[0].hash }],
    history: { window_limit: 2000, page_size: 100, complete: index === 0, truncated: index !== 0,
      window_limit_reached: index === 2, window_status, ref: repo.default_branch, tip: items[0].hash,
      synced_at: updated, total: items.length, has_more: false, items },
    changes: repo.id === fixtureIds.api ? { [head.hash]: changes, [base.hash]: [{ ...changes[0], type: 'added', insertions: 9, deletions: 0 }] } : {},
    comparisons: repo.id === fixtureIds.api ? comparisons : [],
    fileHistory: Object.fromEntries(files.map(({ path }) => [path, (path === searchPath ? items : [items[0]])
      .map((entry) => ({ ...entry, path, change_type: entry.parents.length ? 'M' : 'A' }))])),
    blame: Object.fromEntries(files.map(({ path, content }) => [path, (content ?? '').split('\n').map((line, i) => ({
      ...identity, line: i + 1, original_line: i + 1, commit_hash: items[0].hash,
      summary: items[0].subject, filename: path, boundary: false, content: line,
      authored_at: items[0].authored_at, committed_at: items[0].committed_at,
    }))])),
    activity: activity.filter(({ item }) => item.repository_id === repo.id),
    policies: [{ kind: 'files', enabled: true, interval_seconds: 1800, last_success_at: updated },
      ...(repo.source_type === 'github' ? [{ kind: 'github_activity', enabled: true, interval_seconds: 3600 } as const] : [])],
  }
})

function document(id: string, title: string, content: string, extra: Partial<Document> = {}): Document {
  return { ...timestamps, id, title, content, content_bytes: byteLength(content), scope: 'project',
    version: 1, project_id: fixtureIds.core, ...extra }
}

export const documents = [
  document(fixtureIds.handbook, 'Search operator handbook', '# Search operator handbook\n\nSelect a **search scope** before running a query.\n\n- Project documents cover shared runbooks.\n- Repository scope looks at a single active snapshot.\n\n| Scope | Example |\n| --- | --- |\n| Project | Deployment notes |', { version: 3 }),
  document(fixtureIds.routes, 'Search route contract', '# Search route contract\n\nSpecify the scope and query.\n\n```http\nGET /api/v1/projects/{id}/search?scope=project&q=search\n```',
    { repository_id: fixtureIds.api, scope: 'repository', version: 2 }),
  document(fixtureIds.migration, 'Indexer migration notes', '# Indexer migration notes\n\nThe legacy search indexing runbook is retained for rollback reference.',
    { origin: { repository_id: fixtureIds.retired, repository_name: 'indexer-v1' }, version: 4 }),
]

export const jobTypes = { 'repository.sync': 'Sync files', 'repository.commits.sync': 'Import commits',
  'repository.github_activity.sync': 'Import GitHub activity', 'repository.scan': 'Scan repository' }

export function makeJob(repository: Repository, now: string, id: string = crypto.randomUUID()): Job {
  return { id, type: 'repository.sync', status: 'pending', attempt: 0, max_attempts: 3,
    timeout_seconds: 120, run_at: now, created_at: now, updated_at: now,
    payload: { project_id: repository.project_id, repository_id: repository.id, ref: repository.default_branch } }
}

const jobFixtures: { repository: Repository; status: JobStatus; type: keyof typeof jobTypes; details?: Partial<Job> }[] = [
  { repository: repositories[2], status: 'pending', type: 'repository.scan' },
  { repository: repositories[1], status: 'running', type: 'repository.commits.sync',
    details: { started_at: updated, attempt: 1 } },
  { repository: repositories[0], status: 'succeeded', type: 'repository.sync',
    details: { started_at: '2026-11-24T10:41:55Z', finished_at: updated, attempt: 1, result: { included_files: 3 } } },
  { repository: repositories[1], status: 'failed', type: 'repository.github_activity.sync',
    details: { started_at: '2026-11-24T10:41:30Z', finished_at: updated, attempt: 3,
      last_error_code: 'source_unreachable', last_error: 'The remote source could not be reached.' } },
  { repository: repositories[2], status: 'cancelled', type: 'repository.sync',
    details: { cancel_requested_at: updated, finished_at: updated } },
]

export const jobs = jobFixtures.map(({ repository, status, type, details }, index): Job => ({
  ...makeJob(repository, updated, '40000000-0000-4000-8000-00000000000' + (index + 1)), status, type, ...details,
}))

export function publishSnapshot(preview: RepositoryPreview, job: Job, now: string, snapshotId: string): RepositoryPreview {
  const { repository, files, history, snapshots, policies } = preview
  const parent = snapshots.find((item) => item.status === 'active')
  const next = snapshot(repository, history?.items[0], (parent?.version ?? 0) + 1, 'active', files, now, snapshotId)
  next.job_id = job.id
  next.parent_snapshot_id = parent?.id
  return { ...preview, repository: { ...repository, last_synced_at: now, updated_at: now },
    snapshots: [next, ...snapshots.map((item) => item === parent
      ? { ...item, status: 'superseded' as const, superseded_at: now, updated_at: now } : item)],
    files: files.map((file) => ({ ...file, snapshot_id: next.id })),
    policies: policies.map((policy) => policy.kind === 'files'
      ? { ...policy, last_job_id: job.id, last_success_at: now, last_error_code: null } : policy) }
}

export const health: Health = { status: 'ok', service: 'ProjectLens', env: 'preview', version: '1.0.0', timestamp: updated, uptime: '48h 26m' }
export const readiness: Readiness = { status: 'ok', checks: { postgres: 'ok', redis: 'degraded' } }
export const pingResponse = { message: 'pong' }
