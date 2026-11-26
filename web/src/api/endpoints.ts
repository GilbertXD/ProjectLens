import { ApiError, apiFetch, apiFetchWithMeta } from './client'
import type { ApiConnection } from './connection'
import type {
  BlameLine, CommitComparison, CommitHistoryEntry, CommitHistoryPage, Document, DocumentSummary, FileChange,
  FileHistoryEntry, GitHubActivity, GitRef, Health, Job, JobStatus, KnowledgeScope, Project,
  ProjectStatus, Readiness, Repository, RepositoryRole, RepositorySourceType, SearchCacheStatus, SearchResult,
  SearchScope, SearchSourceType, Snapshot,
  SourceFile, SyncPolicy,
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
export interface RepositorySearchFilters extends PageParams {
  q: string
  source_type?: SearchSourceType[]
  path_prefix?: string
}
export interface SearchFilters extends RepositorySearchFilters {
  scope: SearchScope
  repository_id?: string
  repository_ids?: string[]
  repository_role?: RepositoryRole
  include_project_documents?: boolean
}
export type DocumentInput = Pick<Document, 'title'> & Partial<Pick<Document, 'content'>> & { repository_id?: string | null }
export type DocumentPatch = Partial<Pick<Document, 'title' | 'content'>>
export type DocumentReplacement = Omit<DocumentInput, 'repository_id'>
export type DocumentRecord = Document & { etag?: string }
export const documentMaxBytes = 1_048_576
export interface TreeFilters extends PageParams { path_prefix?: string }
export interface ActiveFileFilters extends TreeFilters { q?: string }
export interface RefFilters extends PageParams { type?: GitRef['type'] }
export interface CommitFilters extends PageParams { ref?: string }
export interface FileHistoryFilters extends CommitFilters { path: string; follow?: boolean }
export interface BlameFilters { path: string; ref?: string; start_line?: number; end_line?: number }
export interface CompareFilters extends PageParams { base: string; head: string }
export interface HistoricalTreeEntry {
  path: string
  blob_oid: string
  mode: string
  size: number
  is_executable: boolean
}
export interface HistoricalFile extends HistoricalTreeEntry {
  commit_hash: string
  sha256: string
  binary: boolean
  line_count: number
  content?: string
}
export interface FileHistoryPage extends Omit<Page<FileHistoryEntry>, 'total'> {
  ref: string
  commit_hash: string
  path: string
  follow: boolean
  has_more: boolean
  shallow: boolean
  history_complete: boolean
  truncated: boolean
}
export interface FileBlame {
  ref: string
  commit_hash: string
  path: string
  start_line: number
  end_line: number
  total_lines: number
  shallow: boolean
  boundary_reached: boolean
  history_complete: boolean
  truncated: boolean
  lines: BlameLine[]
}
export interface CommitFiles { items: FileChange[]; total: number; truncated: boolean }
export type ActivityPage = Omit<Page<GitHubActivity>, 'total'>
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
const documentsPath = (id: string) => projectPath(id) + '/documents'
const documentPath = (id: string, docId: string) => documentsPath(id) + '/' + encodeURIComponent(docId)

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

const commitPath = (id: string, repoId: string, sha: string) =>
  repositoryPath(id, repoId) + '/commits/' + encodeURIComponent(sha)

export const listActiveFiles = (api: ApiConnection, id: string, repoId: string, filters: ActiveFileFilters = {}, signal?: AbortSignal) =>
  apiFetch<Page<SourceFile>>(api, params(repositoryPath(id, repoId) + '/files', filters), { signal })
export const getActiveFileContent = (api: ApiConnection, id: string, repoId: string, path: string, signal?: AbortSignal) =>
  apiFetch<SourceFile & { binary?: boolean }>(api, params(repositoryPath(id, repoId) + '/files/content', { path }), { signal })
export const listRepositoryRefs = (api: ApiConnection, id: string, repoId: string, filters: RefFilters = {}, signal?: AbortSignal) =>
  apiFetch<Page<GitRef> & { default_branch: string }>(api, params(repositoryPath(id, repoId) + '/refs', filters), { signal })
export const listHistoricalTree = (api: ApiConnection, id: string, repoId: string, sha: string, filters: TreeFilters = {}, signal?: AbortSignal) => {
  const { path_prefix, limit, offset } = filters
  return apiFetch<Page<HistoricalTreeEntry> & { commit_hash: string; tree_hash: string; path_prefix: string }>(
    api, params(commitPath(id, repoId, sha) + '/tree', { path_prefix, limit, offset }), { signal })
}
export const getHistoricalFile = (api: ApiConnection, id: string, repoId: string, sha: string, path: string, signal?: AbortSignal) =>
  apiFetch<HistoricalFile>(api, params(commitPath(id, repoId, sha) + '/file', { path }), { signal })
export const listFileHistory = (api: ApiConnection, id: string, repoId: string, filters: FileHistoryFilters, signal?: AbortSignal) =>
  apiFetch<FileHistoryPage>(api, params(repositoryPath(id, repoId) + '/files/history', filters), { signal })

export function validBlameRange(start?: number, end?: number) {
  if (start === undefined && end === undefined) return true
  return start !== undefined && end !== undefined && Number.isInteger(start) && Number.isInteger(end)
    && start > 0 && end >= start && end <= 2147483647 && end - start < 2000
}

export async function getFileBlame(api: ApiConnection, id: string, repoId: string, filters: BlameFilters, signal?: AbortSignal) {
  if (!validBlameRange(filters.start_line, filters.end_line)) {
    throw new Error('Enter both line bounds for a range of at most 2,000 lines.')
  }
  return apiFetch<FileBlame>(api, params(repositoryPath(id, repoId) + '/files/blame', filters), { signal })
}

export const listCommits = (api: ApiConnection, id: string, repoId: string, filters: CommitFilters = {}, signal?: AbortSignal) =>
  apiFetch<CommitHistoryPage>(api, params(repositoryPath(id, repoId) + '/commits', filters), { signal })
export const getCommit = (api: ApiConnection, id: string, repoId: string, sha: string, signal?: AbortSignal) =>
  apiFetch<CommitHistoryEntry>(api, commitPath(id, repoId, sha), { signal })
export const listCommitFiles = (api: ApiConnection, id: string, repoId: string, sha: string, filters: PageParams = {}, signal?: AbortSignal) =>
  apiFetch<CommitFiles>(api, params(commitPath(id, repoId, sha) + '/files', filters), { signal })
export const compareCommits = (api: ApiConnection, id: string, repoId: string, filters: CompareFilters, signal?: AbortSignal) =>
  apiFetch<CommitComparison>(api, params(repositoryPath(id, repoId) + '/compare', filters), { signal })

async function activityPage(api: ApiConnection, path: string, filters: PageParams, signal?: AbortSignal): Promise<ActivityPage> {
  const page = await apiFetch<Omit<ActivityPage, 'items'> & { items?: GitHubActivity[] | null }>(api, params(path, filters), { signal })
  return { ...page, items: page.items ?? [] }
}

export const listProjectIssues = (api: ApiConnection, id: string, filters: PageParams = {}, signal?: AbortSignal) =>
  activityPage(api, projectPath(id) + '/issues', filters, signal)
export const listRepositoryIssues = (api: ApiConnection, id: string, repoId: string, filters: PageParams = {}, signal?: AbortSignal) =>
  activityPage(api, repositoryPath(id, repoId) + '/issues', filters, signal)
export const getIssueDetail = (api: ApiConnection, id: string, repoId: string, number: number, signal?: AbortSignal) =>
  apiFetch<GitHubActivity>(api, repositoryPath(id, repoId) + '/issues/' + number, { signal })
export const listIssueComments = (api: ApiConnection, id: string, repoId: string, number: number, filters: PageParams = {}, signal?: AbortSignal) =>
  activityPage(api, repositoryPath(id, repoId) + '/issues/' + number + '/comments', filters, signal)
export const listProjectPulls = (api: ApiConnection, id: string, filters: PageParams = {}, signal?: AbortSignal) =>
  activityPage(api, projectPath(id) + '/pulls', filters, signal)
export const listRepositoryPulls = (api: ApiConnection, id: string, repoId: string, filters: PageParams = {}, signal?: AbortSignal) =>
  activityPage(api, repositoryPath(id, repoId) + '/pulls', filters, signal)
export const getPullDetail = (api: ApiConnection, id: string, repoId: string, number: number, signal?: AbortSignal) =>
  apiFetch<GitHubActivity>(api, repositoryPath(id, repoId) + '/pulls/' + number, { signal })
export const listPullComments = (api: ApiConnection, id: string, repoId: string, number: number, filters: PageParams = {}, signal?: AbortSignal) =>
  activityPage(api, repositoryPath(id, repoId) + '/pulls/' + number + '/comments', filters, signal)
export const listPullCommits = (api: ApiConnection, id: string, repoId: string, number: number, filters: PageParams = {}, signal?: AbortSignal) =>
  activityPage(api, repositoryPath(id, repoId) + '/pulls/' + number + '/commits', filters, signal)

export const searchScopes: readonly SearchScope[] = ['project', 'repository', 'cross_repository', 'all']

function searchParams({ q, source_type, path_prefix, limit, offset }: RepositorySearchFilters) {
  return { q: q.trim(), source_type: source_type?.join(','), path_prefix: path_prefix?.trim(), limit, offset }
}

export function projectSearchParams(filters: SearchFilters) {
  const { scope, repository_id, repository_ids, repository_role, include_project_documents } = filters
  return {
    ...searchParams(filters),
    scope,
    ...(scope === 'project' && { path_prefix: undefined }),
    ...(scope === 'repository' && { repository_id }),
    ...(scope === 'cross_repository' && {
      repository_ids: repository_ids?.join(','),
      repository_role,
      include_project_documents: include_project_documents === true ? true : undefined,
    }),
  }
}

const cacheStatuses: readonly string[] = ['hit', 'miss', 'bypass', 'degraded']

async function search(api: ApiConnection, path: string, filters: { q: string }, signal?: AbortSignal): Promise<SearchResult> {
  if (!filters.q) throw new Error('Enter a search query.')
  const { data, searchCache, searchVersion } = await apiFetchWithMeta<SearchResult>(api, params(path, filters), { signal })
  return {
    ...data,
    cache_status: searchCache && cacheStatuses.includes(searchCache) ? searchCache as SearchCacheStatus : data.cache_status,
    search_version: searchVersion && /^\d+$/.test(searchVersion) ? Number(searchVersion) : data.search_version,
  }
}

export async function searchProject(api: ApiConnection, id: string, filters: SearchFilters, signal?: AbortSignal) {
  if (!searchScopes.includes(filters.scope)) throw new Error('Choose a search scope.')
  if (filters.scope === 'repository' && !filters.repository_id) throw new Error('Choose a repository.')
  return search(api, projectPath(id) + '/search', projectSearchParams(filters), signal)
}

export const searchRepository = (api: ApiConnection, id: string, repoId: string, filters: RepositorySearchFilters, signal?: AbortSignal) =>
  search(api, repositoryPath(id, repoId) + '/search', searchParams(filters), signal)

export const listDocuments = (api: ApiConnection, id: string, filters: DocumentFilters = {}, signal?: AbortSignal) =>
  apiFetch<Page<DocumentSummary>>(api, params(documentsPath(id), {
    ...filters, repository_id: filters.scope === 'project' ? undefined : filters.repository_id,
  }), { signal })

async function fetchDocument(api: ApiConnection, path: string, options?: RequestInit): Promise<DocumentRecord> {
  const { data, etag } = await apiFetchWithMeta<Document>(api, path, options)
  return { ...data, etag }
}

export const getDocument = (api: ApiConnection, id: string, docId: string, signal?: AbortSignal) =>
  fetchDocument(api, documentPath(id, docId), { signal })

export function createDocument(api: ApiConnection, id: string, { title, content, repository_id }: DocumentInput) {
  const path = repository_id ? repositoryPath(id, repository_id) + '/documents' : documentsPath(id)
  return fetchDocument(api, path, json('POST', { title, content, repository_id: repository_id || undefined }))
}

const ifMatch = (version: number) => ({ 'If-Match': '"' + version + '"' })

function writeDocument(api: ApiConnection, id: string, docId: string, method: 'PATCH' | 'PUT',
  { title, content }: DocumentPatch, version: number) {
  const request = json(method, { title, content, version })
  return fetchDocument(api, documentPath(id, docId), { ...request, headers: { ...request.headers, ...ifMatch(version) } })
}

export const patchDocument = (api: ApiConnection, id: string, docId: string, input: DocumentPatch, version: number) =>
  writeDocument(api, id, docId, 'PATCH', input, version)
export const replaceDocument = (api: ApiConnection, id: string, docId: string, input: DocumentReplacement, version: number) =>
  writeDocument(api, id, docId, 'PUT', input, version)
export const deleteDocument = (api: ApiConnection, id: string, docId: string, version?: number) =>
  apiFetch<void>(api, documentPath(id, docId), { method: 'DELETE', headers: version === undefined ? undefined : ifMatch(version) })

export const listJobs = (api: ApiConnection, filters: JobFilters = {}, signal?: AbortSignal) =>
  apiFetch<Page<Job>>(api, params(jobsPath, filters), { signal })
export const getJob = (api: ApiConnection, id: string, signal?: AbortSignal) =>
  apiFetch<Job>(api, jobPath(id), { signal })
export const cancelJob = (api: ApiConnection, id: string) =>
  apiFetch<Job>(api, jobPath(id) + '/cancel', { method: 'POST' })
