import { vi } from 'vitest'
import { documents, emptyRepository, health, jobs, makeJob, projects, publishSnapshot, repositoryPreviews } from '@/mocks/fixtures'
import type { Job, Project, Repository } from '@/types/domain'

export const json = (body: unknown, status = 200, headers?: HeadersInit) => Response.json(body, { status, headers })
export const problem = (status: number, code: string, error: string, headers?: HeadersInit) => json({ error, code }, status, headers)

export function mockApi(override?: (url: URL, method: string, init?: RequestInit) => Response | undefined) {
  let projectList = structuredClone(projects)
  let repos = structuredClone(repositoryPreviews)
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
        }
      }
    }

    return problem(404, 'not_found', 'not found')
  })
  vi.stubGlobal('fetch', fetch)
  return fetch
}
