import { queryOptions, useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError } from './client'
import { useConnection } from './ConnectionProvider'
import type { ApiConnection } from './connection'
import * as endpoints from './endpoints'
import type { Job, Project, Repository, SyncPolicy } from '@/types/domain'

const key = (api: ApiConnection) => [api.apiBaseUrl, api.revision] as const
const projectsKey = (api: ApiConnection) => [...key(api), 'projects'] as const
const projectKey = (api: ApiConnection, id: string) => [...key(api), 'project', id] as const
const repositoriesKey = (api: ApiConnection, id: string) => [...projectKey(api, id), 'repositories'] as const
const repositoryKey = (api: ApiConnection, id: string, repoId: string) => [...projectKey(api, id), 'repository', repoId] as const
const policiesKey = (api: ApiConnection, id: string, repoId: string) => [...repositoryKey(api, id, repoId), 'policies'] as const
const jobsKey = (api: ApiConnection) => [...key(api), 'jobs'] as const
const recentJobsKey = (api: ApiConnection) => [...jobsKey(api), 'recent-project'] as const

export const jobPollInterval = 1500
export const canPoll = (error: Error | null) => !(error instanceof ApiError
  && (error.code === 'invalid_token' || (error.status >= 400 && error.status < 500)))
export const isActiveJob = (job: Pick<Job, 'status'>) => job.status === 'pending' || job.status === 'running'
export const jobTypes: Readonly<Record<string, string>> = {
  'repository.sync': 'Sync files',
  'repository.scan': 'Scan repository',
  'repository.commits.sync': 'Import commits',
  'repository.github_activity.sync': 'Import GitHub activity',
}

export const jobLabel = (type: string) => Object.hasOwn(jobTypes, type) ? jobTypes[type] : type

export function useDiagnostics() {
  const { connection: api } = useConnection()
  const health = useQuery({ queryKey: [...key(api), 'health'], queryFn: ({ signal }) => endpoints.getHealth(api, signal) })
  const readiness = useQuery({ queryKey: [...key(api), 'readiness'], queryFn: ({ signal }) => endpoints.getReadiness(api, signal) })
  const ping = useMutation({ mutationFn: () => endpoints.pingApi(api) })
  return { health, readiness, ping }
}

export function useProjects(filters: endpoints.ProjectFilters) {
  const { connection: api } = useConnection()
  return useQuery({
    queryKey: [...projectsKey(api), filters],
    queryFn: ({ signal }) => endpoints.listProjects(api, filters, signal),
  })
}

export function useProjectQuery(id: string) {
  const { connection: api } = useConnection()
  return useQuery({
    queryKey: projectKey(api, id), enabled: Boolean(id),
    queryFn: ({ signal }) => endpoints.getProject(api, id, signal),
  })
}

export function useSaveProject(current?: Project) {
  const { connection: api } = useConnection()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: endpoints.ProjectInput) => {
      if (!current) return endpoints.createProject(api, input)
      const { slug, name, description, status } = input
      const patch: endpoints.ProjectPatch = {
        slug: slug !== current.slug ? slug : undefined,
        name: name !== current.name ? name : undefined,
        description: description !== current.description ? description : undefined,
        status: status !== current.status ? status : undefined,
      }
      if (Object.values(patch).every((field) => field === undefined)) return Promise.resolve(current)
      return endpoints.patchProject(api, current.id, patch)
    },
    async onSuccess(project) {
      await client.cancelQueries({ queryKey: projectKey(api, project.id), exact: true })
      client.setQueryData(projectKey(api, project.id), project)
      await client.invalidateQueries({ queryKey: projectsKey(api) })
    },
  })
}

export function useArchiveProject() {
  const { connection: api } = useConnection()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (project: Project) => endpoints.patchProject(api, project.id, {
      status: project.status === 'active' ? 'archived' : 'active',
    }),
    async onSuccess(project) {
      await client.cancelQueries({ queryKey: projectKey(api, project.id), exact: true })
      client.setQueryData(projectKey(api, project.id), project)
      await client.invalidateQueries({ queryKey: projectsKey(api) })
    },
  })
}

export function useDeleteProject() {
  const { connection: api } = useConnection()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => endpoints.deleteProject(api, id),
    async onSuccess(_, id) {
      client.removeQueries({ queryKey: projectKey(api, id) })
      await Promise.all([projectsKey(api), jobsKey(api)].map((queryKey) => client.invalidateQueries({ queryKey })))
    },
  })
}

export function useRepositories(id: string, filters: endpoints.RepositoryFilters) {
  const { connection: api } = useConnection()
  return useQuery({
    queryKey: [...repositoriesKey(api, id), filters],
    queryFn: ({ signal }) => endpoints.listRepositories(api, id, filters, signal),
  })
}

export function useRepository(id: string, repoId: string) {
  const { connection: api } = useConnection()
  return useQuery({
    queryKey: repositoryKey(api, id, repoId), enabled: Boolean(repoId),
    queryFn: ({ signal }) => endpoints.getRepository(api, id, repoId, signal),
  })
}

export function useAssociateRepository(id: string) {
  const { connection: api } = useConnection()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: endpoints.RepositoryInput) => endpoints.associateRepository(api, id, input),
    onSuccess: () => client.invalidateQueries({ queryKey: repositoriesKey(api, id) }),
  })
}

export function useRepositorySettings(id: string, repoId: string) {
  const { connection: api } = useConnection()
  const client = useQueryClient()
  const queryKey = repositoryKey(api, id, repoId)
  async function update(repository: Repository) {
    await client.cancelQueries({ queryKey, exact: true })
    client.setQueryData(queryKey, repository)
    await client.invalidateQueries({ queryKey: repositoriesKey(api, id) })
  }
  const save = useMutation({ mutationFn: (input: endpoints.RepositoryPatch) => endpoints.patchRepository(api, id, repoId, input), onSuccess: update })
  const refresh = useMutation({ mutationFn: () => endpoints.refreshRepositoryMetadata(api, id, repoId), onSuccess: update })
  const remove = useMutation({
    mutationFn: () => endpoints.deleteRepository(api, id, repoId),
    async onSuccess() {
      // Remove cached detail and child queries for the deleted association.
      client.removeQueries({ queryKey })
      await Promise.all([
        repositoriesKey(api, id), [...projectKey(api, id), 'documents'], jobsKey(api),
      ].map((key) => client.invalidateQueries({ queryKey: key })))
    },
  })
  return { save, refresh, remove }
}

function policyOptions(api: ApiConnection, id: string, repoId: string) {
  return queryOptions({
    queryKey: policiesKey(api, id, repoId),
    queryFn: ({ signal }) => endpoints.listSyncPolicies(api, id, repoId, signal),
  })
}

export function usePolicies(id: string, repoId: string) {
  const { connection: api } = useConnection()
  return useQuery(policyOptions(api, id, repoId))
}

export function useRepositoryPolicies(id: string, repositories: Repository[]) {
  const { connection: api } = useConnection()
  return useQueries({ queries: repositories.map((repo) => policyOptions(api, id, repo.id)) })
}

export function useSetPolicy(id: string, repoId: string, kind: SyncPolicy['kind']) {
  const { connection: api } = useConnection()
  const client = useQueryClient()
  const queryKey = policiesKey(api, id, repoId)
  return useMutation({
    mutationFn: (input: endpoints.PolicyInput) => endpoints.setSyncPolicy(api, id, repoId, kind, input),
    onSuccess: () => client.invalidateQueries({ queryKey }),
  })
}

export function useSnapshots(id: string, repoId: string, filters: endpoints.PageParams) {
  const { connection: api } = useConnection()
  const queryKey = repositoryKey(api, id, repoId)
  const snapshots = useQuery({
    queryKey: [...queryKey, 'snapshots', filters],
    queryFn: ({ signal }) => endpoints.listSnapshots(api, id, repoId, filters, signal),
  })
  const active = useQuery({
    queryKey: [...queryKey, 'active-snapshot'],
    queryFn: ({ signal }) => endpoints.getActiveSnapshot(api, id, repoId, signal),
  })
  return { snapshots, active }
}

export function useDocuments(id: string, filters: endpoints.DocumentFilters) {
  const { connection: api } = useConnection()
  return useQuery({
    queryKey: [...projectKey(api, id), 'documents', filters],
    queryFn: ({ signal }) => endpoints.listDocuments(api, id, filters, signal),
  })
}

export function useRecentProjectJobs(id: string) {
  const { connection: api } = useConnection()
  return useQuery({
    queryKey: [...recentJobsKey(api), id],
    async queryFn({ signal }) {
      const jobs: Job[] = []
      let offset = 0
      while (jobs.length < 5) {
        const page = await endpoints.listJobs(api, { limit: 50, offset }, signal)
        jobs.push(...page.items.filter((job) => job.payload.project_id === id))
        const next = page.offset + page.items.length
        if (!page.items.length || next <= offset || next >= page.total) break
        offset = next
      }
      return jobs.slice(0, 5)
    },
    refetchInterval: (query) => canPoll(query.state.error) && query.state.data?.some(isActiveJob) ? jobPollInterval : false,
  })
}

export function useJobs(filters: endpoints.JobFilters) {
  const { connection: api } = useConnection()
  return useQuery({
    queryKey: [...jobsKey(api), 'list', filters],
    queryFn: ({ signal }) => endpoints.listJobs(api, filters, signal),
    refetchInterval: (query) => canPoll(query.state.error) && query.state.data?.items.some(isActiveJob) ? jobPollInterval : false,
  })
}

export function jobOptions(api: ApiConnection, id: string) {
  return queryOptions({
    queryKey: [...jobsKey(api), 'detail', id],
    queryFn: ({ signal }) => endpoints.getJob(api, id, signal),
  })
}

export function useCancelJob() {
  const { connection: api } = useConnection()
  const client = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => endpoints.cancelJob(api, id),
    async onSuccess(job) {
      const { queryKey } = jobOptions(api, job.id)
      await client.cancelQueries({ queryKey })
      client.setQueryData(queryKey, job)
      await client.invalidateQueries({ queryKey: [...jobsKey(api), 'list'] })
      await client.invalidateQueries({ queryKey: recentJobsKey(api) })
    },
  })
}

const enqueue = {
  files: endpoints.enqueueFileSync,
  commits: endpoints.enqueueCommitsSync,
  github_activity: endpoints.enqueueGitHubActivitySync,
}

export function useEnqueueSync(id: string, repoId: string, kind: SyncPolicy['kind']) {
  const { connection: api } = useConnection()
  const client = useQueryClient()
  const mutation = useMutation({
    mutationKey: [...repositoryKey(api, id, repoId), 'enqueue', kind],
    mutationFn: () => enqueue[kind](api, id, repoId),
    onSuccess: () => { void client.invalidateQueries({ queryKey: jobsKey(api) }) },
  })
  function refreshOnSettled() {
    void client.invalidateQueries({ queryKey: repositoryKey(api, id, repoId) })
    void client.invalidateQueries({ queryKey: repositoriesKey(api, id) })
    void client.invalidateQueries({ queryKey: recentJobsKey(api) })
  }
  return { mutation, receipt: mutation.data, refreshOnSettled }
}
