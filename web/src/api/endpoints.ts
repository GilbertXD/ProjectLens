import { ApiError, apiFetch, apiFetchWithMeta } from './client'
import type { ApiConnection } from './connection'
import type {
  DocumentSummary, Health, Job, JobStatus, KnowledgeScope, Project, ProjectStatus, Readiness,
  Repository, RepositoryRole, RepositorySourceType, Snapshot, SyncPolicy,
} from '@/types/domain'

export interface Page<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

export interface PageParams { limit?: number; offset?: number }
export interface ProjectFilters extends PageParams { status?: ProjectStatus }
export interface RepositoryFilters extends PageParams { role?: RepositoryRole; source_type?: RepositorySourceType }
export interface JobFilters extends PageParams { status?: JobStatus; type?: string }
export interface DocumentFilters extends PageParams { scope?: KnowledgeScope; repository_id?: string }
export type ProjectInput = Pick<Project, 'slug' | 'name'> & Partial<Pick<Project, 'description' | 'status'>>
export type ProjectPatch = Partial<ProjectInput>
export type RepositoryInput = Pick<Repository, 'source' | 'source_type'> & Partial<Pick<Repository, 'name' | 'description' | 'role'>>
export type RepositoryPatch = Partial<Pick<Repository, 'name' | 'description' | 'role'>>
export type PolicyInput = Pick<SyncPolicy, 'enabled' | 'interval_seconds'>
interface PolicyList { items: SyncPolicy[] }
export interface SyncReceipt { job_id: string; status: JobStatus; poll_url: string; reused: boolean }

const projectsPath = '/api/v1/projects'
const jobsPath = '/api/v1/jobs'
const projectPath = (id: string) => projectsPath + '/' + encodeURIComponent(id)
const repositoriesPath = (id: string) => projectPath(id) + '/repositories'
const repositoryPath = (id: string, repoId: string) => repositoriesPath(id) + '/' + encodeURIComponent(repoId)
const jobPath = (id: string) => jobsPath + '/' + encodeURIComponent(id)

function params(path: string, filters: object) {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(filters)) {
    if (value !== undefined && value !== '') query.set(key, String(value))
  }
  return query.size ? path + '?' + query : path
}

function json(method: 'POST' | 'PATCH' | 'PUT', body: object) {
  return { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
}

export const getHealth = (api: ApiConnection, signal?: AbortSignal) => apiFetch<Health>(api, '/healthz', { signal })
export const getReadiness = (api: ApiConnection, signal?: AbortSignal) => apiFetch<Readiness>(api, '/readyz', {
  signal,
  acceptError: (status, body) => {
    const ready = body as Partial<Readiness> | null
    return status === 503 && ready?.status === 'unavailable' && ready.checks?.postgres === 'unavailable'
      && ['ok', 'disabled', 'degraded'].includes(ready.checks.redis)
  },
})
export const pingApi = (api: ApiConnection) => apiFetch<{ message: string }>(api, '/api/v1/ping')

export const listProjects = (api: ApiConnection, filters: ProjectFilters = {}, signal?: AbortSignal) =>
  apiFetch<Page<Project>>(api, params(projectsPath, filters), { signal })
export const getProject = (api: ApiConnection, id: string, signal?: AbortSignal) =>
  apiFetch<Project>(api, projectPath(id), { signal })
export const createProject = (api: ApiConnection, input: ProjectInput) =>
  apiFetch<Project>(api, projectsPath, json('POST', input))
export const patchProject = (api: ApiConnection, id: string, input: ProjectPatch) =>
  apiFetch<Project>(api, projectPath(id), json('PATCH', input))
export const deleteProject = (api: ApiConnection, id: string) =>
  apiFetch<void>(api, projectPath(id), { method: 'DELETE' })

export const listRepositories = (api: ApiConnection, id: string, filters: RepositoryFilters = {}, signal?: AbortSignal) =>
  apiFetch<Page<Repository>>(api, params(repositoriesPath(id), filters), { signal })
export const getRepository = (api: ApiConnection, id: string, repoId: string, signal?: AbortSignal) =>
  apiFetch<Repository>(api, repositoryPath(id, repoId), { signal })
export const associateRepository = (api: ApiConnection, id: string, input: RepositoryInput) =>
  apiFetch<Repository>(api, repositoriesPath(id), json('POST', input))
export const patchRepository = (api: ApiConnection, id: string, repoId: string, input: RepositoryPatch) =>
  apiFetch<Repository>(api, repositoryPath(id, repoId), json('PATCH', input))
export const deleteRepository = (api: ApiConnection, id: string, repoId: string) =>
  apiFetch<void>(api, repositoryPath(id, repoId), { method: 'DELETE' })
export const refreshRepositoryMetadata = (api: ApiConnection, id: string, repoId: string) =>
  apiFetch<Repository>(api, repositoryPath(id, repoId) + '/refresh-metadata', { method: 'POST' })

async function enqueueSync(api: ApiConnection, path: string, input?: { ref?: string }): Promise<SyncReceipt> {
  const response = await apiFetchWithMeta<Omit<SyncReceipt, 'reused'>>(api, path, input ? json('POST', input) : { method: 'POST' })
  return { ...response.data, reused: response.status === 200 }
}
export const enqueueFileSync = (api: ApiConnection, id: string, repoId: string, ref?: string) =>
  enqueueSync(api, repositoryPath(id, repoId) + '/sync', { ref })
export const enqueueCommitsSync = (api: ApiConnection, id: string, repoId: string, ref?: string) =>
  enqueueSync(api, repositoryPath(id, repoId) + '/commits/sync', { ref })
export const enqueueGitHubActivitySync = (api: ApiConnection, id: string, repoId: string) =>
  enqueueSync(api, repositoryPath(id, repoId) + '/github-activity/sync')

export const listSyncPolicies = (api: ApiConnection, id: string, repoId: string, signal?: AbortSignal) =>
  apiFetch<PolicyList>(api, repositoryPath(id, repoId) + '/sync-policy', { signal })
export const setSyncPolicy = (api: ApiConnection, id: string, repoId: string, kind: SyncPolicy['kind'], input: PolicyInput) =>
  apiFetch<PolicyList>(api, repositoryPath(id, repoId) + '/sync-policy/' + kind, json('PUT', input))

export const listSnapshots = (api: ApiConnection, id: string, repoId: string, filters: PageParams = {}, signal?: AbortSignal) =>
  apiFetch<Page<Snapshot>>(api, params(repositoryPath(id, repoId) + '/snapshots', filters), { signal })
export async function getActiveSnapshot(api: ApiConnection, id: string, repoId: string, signal?: AbortSignal) {
  try {
    return await apiFetch<Snapshot>(api, repositoryPath(id, repoId) + '/snapshots/active', { signal })
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) return null
    throw error
  }
}

export const listDocuments = (api: ApiConnection, id: string, filters: DocumentFilters = {}, signal?: AbortSignal) =>
  apiFetch<Page<DocumentSummary>>(api, params(projectPath(id) + '/documents', filters), { signal })

export const listJobs = (api: ApiConnection, filters: JobFilters = {}, signal?: AbortSignal) =>
  apiFetch<Page<Job>>(api, params(jobsPath, filters), { signal })
export const getJob = (api: ApiConnection, id: string, signal?: AbortSignal) =>
  apiFetch<Job>(api, jobPath(id), { signal })
export const cancelJob = (api: ApiConnection, id: string) =>
  apiFetch<Job>(api, jobPath(id) + '/cancel', { method: 'POST' })
