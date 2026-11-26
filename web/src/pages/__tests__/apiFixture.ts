import { vi } from 'vitest'
import { documents, emptyRepository, fixtureIds, health, jobs, makeJob, projects, publishSnapshot, repositoryPreviews } from '@/mocks/fixtures'
import type { Job, Project, Repository } from '@/types/domain'

export const json = (body: unknown, status = 200, headers?: HeadersInit) => Response.json(body, { status, headers })
export const problem = (status: number, code: string, error: string, headers?: HeadersInit) => json({ error, code }, status, headers)

export function mockApi(
  override?: (url: URL, method: string, init?: RequestInit) => Response | undefined,
  { additionalIssues = 0 } = {},
) {
  let projectList = structuredClone(projects)
  let repos = structuredClone(repositoryPreviews)
  const github = repos.find(({ repository }) => repository.id === fixtureIds.gin)!
  const issue = github.activity.find(({ item }) => item.kind === 'issue')!.item
  github.activity.push(...Array.from({ length: additionalIssues }, (_, index) => ({
    item: { ...issue, number: 5000 + index, title: `Fixture issue ${index + 1}` }, comments: [], commits: [],
  })))
  let jobList = structuredClone(jobs)
  const queued = new Map<string, { reads: number; suffix: string }>()
  const fetch = vi.fn(async (input: string, init?: RequestInit): Promise<Response> => {
    const url = new URL(input, 'http://localhost')
    const { pathname, searchParams } = url
    const method = init?.method ?? 'GET'
    const time = new Date().toISOString()
    const intercepted = override?.(url, method, init)
    if (intercepted) return intercepted
    const body = init?.body ? JSON.parse(String(init.body)) : {}
    const [resource, id, group, repoId, ...tail] = pathname.split('/').slice(3)
    const suffix = tail.join('/')
    const limit = Number(searchParams.get('limit') ?? 20)
    const offset = Number(searchParams.get('offset') ?? 0)
    const page = <T,>(items: T[]) => json({ items: items.slice(offset, offset + limit), total: items.length, limit, offset })
    const activityPage = <T,>(items: T[]) => json({ items: items.slice(offset, offset + limit), limit, offset })
    const included = (name: string, value: string) => !searchParams.has(name) || searchParams.get(name) === value

    if (pathname === '/healthz') return json(health)
    if (pathname === '/readyz') return json({ status: 'unavailable', checks: { postgres: 'unavailable', redis: 'degraded' } }, 503)
    if (pathname === '/api/v1/ping') return json({ message: 'pong' })

    if (resource === 'jobs') {
      if (!id) return page(jobList.filter((job) => included('status', job.status) && included('type', job.type)))
      let job = jobList.find((item) => item.id === id)
      if (job && group === 'cancel' && method === 'POST') {
        job = { ...job, status: 'cancelled', finished_at: time, cancel_requested_at: time }
        jobList = jobList.map((item) => item.id === id ? job! : item)
        queued.delete(id)
      }
      const progress = queued.get(id)
      if (job && progress) {
        progress.reads += 1
        if (progress.reads === 1) {
          job = { ...job, status: 'running', attempt: 1, started_at: time }
        } else {
          const snapshotId = crypto.randomUUID()
          if (progress.suffix === 'sync') {
            repos = repos.map((repo) => repo.repository.id === job!.payload.repository_id
              ? publishSnapshot(repo, job!, time, snapshotId) : repo)
          }
          job = { ...job, status: 'succeeded', finished_at: time, result: { snapshot_id: snapshotId } }
          queued.delete(id)
        }
        jobList = jobList.map((item) => item.id === id ? job! : item)
      }
      if (job) return json(job)
    }

    if (resource === 'projects') {
      const project = projectList.find((item) => item.id === id)
      if (!id && method === 'GET') return page(projectList.filter((item) => included('status', item.status)))
      if ((!id && method === 'POST') || (project && !group && method === 'PATCH')) {
        if (body.slug && projectList.some((item) => item.id !== id && item.slug === body.slug)) {
          return problem(409, 'conflict', 'project slug already exists', { 'X-Request-ID': 'project-conflict' })
        }
        const updated: Project = { id: crypto.randomUUID(), created_at: time, description: '', status: 'active',
          ...project, ...body, updated_at: time }
        projectList = project ? projectList.map((item) => item === project ? updated : item) : [updated, ...projectList]
        return json(updated, project ? 200 : 201)
      }
      if (project && !group && method === 'DELETE') {
        projectList = projectList.filter((item) => item !== project)
        return new Response(null, { status: 204 })
      }
      if (project && !group) return json(project)
      if (project && group === 'documents') return page(documents.filter((doc) => doc.project_id === id))
      const threads = repos.filter(({ repository }) => repository.project_id === id)
        .flatMap(({ activity }) => activity)
      const discussionKind = (resource: string) => resource === 'issues' ? 'issue' : 'pull_request'
      if (project && (group === 'issues' || group === 'pulls')) {
        return activityPage(threads.filter(({ item }) => item.kind === discussionKind(group)).map(({ item }) => item))
      }

      if (project && group === 'repositories') {
        const preview = repos.find(({ repository }) => repository.project_id === id && repository.id === repoId)
        if (!repoId && method === 'GET') {
          return page(repos.map(({ repository }) => repository).filter((repo) =>
            repo.project_id === id && included('role', repo.role) && included('source_type', repo.source_type)))
        }
        if (!repoId && method === 'POST') {
          const name = body.name ?? body.source.replace(/^https:\/\/github\.com\//, '')
          const repo: Repository = { ...body, name, project_id: id, id: crypto.randomUUID(), canonical_source: body.source,
            default_branch: 'main', created_at: time, updated_at: time }
          repos = [emptyRepository(repo), ...repos]
          return json(repo, 201)
        }
        if (preview) {
          if (!suffix && method === 'GET') return json(preview.repository)
          if (!suffix && method === 'DELETE') {
            repos = repos.filter((entry) => entry !== preview)
            return new Response(null, { status: 204 })
          }
          if ((!suffix && method === 'PATCH') || suffix === 'refresh-metadata') {
            preview.repository = { ...preview.repository, ...body, updated_at: time }
            return json(preview.repository)
          }
          if (suffix === 'snapshots') return page(preview.snapshots)
          if (suffix === 'snapshots/active') {
            const active = preview.snapshots.find((snapshot) => snapshot.status === 'active')
            return active ? json(active) : problem(404, 'not_found', 'not found')
          }
          if (suffix === 'sync-policy') return json({ items: preview.policies })
          if (suffix.startsWith('sync-policy/') && method === 'PUT') {
            const kind = tail[1]
            preview.policies = [...preview.policies.filter((policy) => policy.kind !== kind), { kind, ...body }]
            return json({ items: preview.policies })
          }
          if (['sync', 'commits/sync', 'github-activity/sync'].includes(suffix) && method === 'POST') {
            const type = suffix.replace('github-activity', 'github_activity').replace('/', '.')
            const existing = jobList.find((job) => job.payload.repository_id === repoId
              && job.type === 'repository.' + type && queued.has(job.id))
            const job: Job = existing ?? { ...makeJob(preview.repository, time), type: 'repository.' + type }
            if (!existing) {
              queued.set(job.id, { reads: 0, suffix })
              jobList = [job, ...jobList]
            }
            return json({ job_id: job.id, status: job.status, poll_url: '/api/v1/jobs/' + job.id }, existing ? 200 : 202)
          }
          if (method === 'GET') {
            const active = preview.snapshots.find((snapshot) => snapshot.status === 'active')
            const activeFiles = preview.files.filter((file) => file.snapshot_id === active?.id)
            const path = searchParams.get('path') ?? ''
            const prefix = searchParams.get('path_prefix') ?? ''
            const activeFile = activeFiles.find((file) => file.path === path)
            const history = preview.history
            const invalidBlank = ['ref', 'type', 'path_prefix', 'follow', 'start_line', 'end_line']
              .find((name) => searchParams.get(name) === '')
            if (invalidBlank) return problem(400, 'invalid_input', 'blank optional parameter ' + invalidBlank)
            if (suffix === 'files') {
              const q = searchParams.get('q')?.toLowerCase() ?? ''
              return page(activeFiles.filter((file) => file.path.startsWith(prefix)
                && (file.path.toLowerCase().includes(q) || file.content?.toLowerCase().includes(q))))
            }
            if (suffix === 'files/content' && activeFile) return json(activeFile)
            if (suffix === 'refs') {
              const items = preview.refs.filter((ref) => included('type', ref.type))
              return json({ items: items.slice(offset, offset + limit), total: items.length, limit, offset,
                default_branch: preview.repository.default_branch })
            }
            if (suffix === 'files/history' && activeFile) {
              const items = preview.fileHistory[path] ?? []
              return json({ items: items.slice(offset, offset + limit), has_more: offset + limit < items.length,
                shallow: false, history_complete: offset + limit >= items.length, limit, offset,
                path, commit_hash: active?.commit_hash, follow: searchParams.get('follow') === 'true' })
            }
            if (suffix === 'files/blame' && activeFile) {
              const lines = preview.blame[path] ?? []
              const start = Number(searchParams.get('start_line') ?? 1)
              const end = Math.min(Number(searchParams.get('end_line') ?? lines.length), lines.length)
              return json({ lines: lines.slice(start - 1, end), path, commit_hash: active?.commit_hash, total_lines: lines.length,
                start_line: start, end_line: end, shallow: false, boundary_reached: false, truncated: false, history_complete: true })
            }
            if (suffix === 'commits' && history) {
              return json({ ...history, ref: searchParams.get('ref') ?? 'HEAD', items: history.items.slice(offset, offset + limit) })
            }
            if (tail[0] === 'commits') {
              const commit = history?.items.find((item) => item.hash === tail[1])
              if (commit && !tail[2]) return json(commit)
              if (commit && tail[2] === 'files') {
                const items = preview.changes[commit.hash] ?? []
                return json({ items: items.slice(offset, offset + limit), total: items.length, truncated: false })
              }
              const treeEntries = activeFiles.map(({ path, blob_oid, mode, size_bytes, is_executable }) =>
                ({ path, blob_oid, mode, size: size_bytes, is_executable }))
              if (commit && tail[2] === 'tree') {
                const items = treeEntries.filter((file) => file.path.startsWith(prefix))
                return json({ items: items.slice(offset, offset + limit), total: items.length, limit, offset,
                  commit_hash: commit.hash, tree_hash: commit.tree_hash, path_prefix: prefix })
              }
              if (commit && tail[2] === 'file' && activeFile) {
                return json({ ...treeEntries.find((file) => file.path === path), commit_hash: commit.hash, binary: false,
                  line_count: activeFile.line_count, sha256: activeFile.sha256, content: activeFile.content })
              }
            }
            if (suffix === 'compare') {
              const comparison = preview.comparisons.find((item) =>
                item.base === searchParams.get('base') && item.head === searchParams.get('head'))
              if (comparison) return json({ ...comparison, items: comparison.items.slice(offset, offset + limit) })
            }
            if (tail[0] === 'issues' || tail[0] === 'pulls') {
              const kind = discussionKind(tail[0])
              const matching = preview.activity.filter(({ item }) => item.kind === kind)
              if (!tail[1]) return activityPage(matching.map(({ item }) => item))
              const thread = matching.find(({ item }) => item.number === Number(tail[1]))
              if (thread && !tail[2]) return json(thread.item)
              if (thread && tail[2] === 'comments') return activityPage(thread.comments)
              if (thread && tail[2] === 'commits' && kind === 'pull_request') {
                return activityPage(thread.commits.map((commit) => ({ kind: 'commit', sha: commit.hash,
                  title: commit.subject, repository_id: repoId, number: thread.item.number })))
              }
            }
          }
        }
      }
    }

    return problem(404, 'not_found', 'not found')
  })
  vi.stubGlobal('fetch', fetch)
  return fetch
}
