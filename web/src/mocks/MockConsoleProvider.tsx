import { createContext, useContext, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import type { Document, Job, Project, Repository, SyncPolicy } from '@/types/domain'
import {
  byteLength, documents, emptyRepository, jobs, makeJob, projects, publishSnapshot, repositoryPreviews,
} from './fixtures'
import type { RepositoryPreview } from './fixtures'

const initialState = { projects, repositories: repositoryPreviews, documents, jobs, searchVersion: 12 }
export type ConsoleData = typeof initialState
type ProjectInput = Omit<Project, 'id' | 'created_at' | 'updated_at'>
type RepositoryInput = Pick<Repository, 'name' | 'description' | 'role' | 'source_type' | 'source'>
type DocumentDraft = Pick<Document, 'title' | 'content'>
const originKey = 'projectlens.apiOrigin'
const now = () => new Date().toISOString()
export const isActiveJob = (job: Job) => job.status === 'pending' || job.status === 'running'

function cancel(job: Job, time: string): Job {
  return { ...job, status: 'cancelled', cancel_requested_at: time, finished_at: time, updated_at: time }
}

function useConsoleStore() {
  const [data, setData] = useState(initialState)
  const [simulatedJobs, setSimulatedJobs] = useState<string[]>([])
  const [apiOrigin, setApiOrigin] = useState(() => localStorage.getItem(originKey) ?? '')
  const [token, setToken] = useState('')

  useEffect(() => {
    const active = data.jobs.filter((job) => simulatedJobs.includes(job.id) && isActiveJob(job))
    const timers = active.map((job) => {
      const running = job.status === 'running'
      const started = Date.parse(job.started_at ?? job.created_at)
      const deadline = started + (running ? 1800 : 700)
      return window.setTimeout(() => {
        const time = now()
        const snapshotId = running ? crypto.randomUUID() : ''
        setData((current) => {
          const latest = current.jobs.find((item) => item.id === job.id)
          if (!latest || latest.status !== job.status) return current
          const target = current.repositories.find(({ repository }) => repository.id === job.payload.repository_id)
          if (!running) {
            return { ...current, jobs: current.jobs.map((item) => item === latest
              ? { ...item, status: 'running', attempt: 1, started_at: time, updated_at: time } : item) }
          }
          if (!target) return current
          const published = publishSnapshot(target, job, time, snapshotId)
          return {
            ...current, searchVersion: current.searchVersion + 1,
            repositories: current.repositories.map((item) => item === target ? published : item),
            jobs: current.jobs.map((item) => item === latest ? { ...item, status: 'succeeded',
              finished_at: time, updated_at: time, result: { snapshot_id: published.snapshots[0].id,
                included_files: published.files.length } } : item),
          }
        })
        if (running) setSimulatedJobs((ids) => ids.filter((id) => id !== job.id))
      }, Math.max(0, deadline - Date.now()))
    })
    return () => timers.forEach(window.clearTimeout)
  }, [data.jobs, simulatedJobs])

  function updateRepository(id: string, change: (preview: RepositoryPreview) => RepositoryPreview, affectsSearch = false) {
    setData((current) => ({ ...current,
      searchVersion: current.searchVersion + Number(affectsSearch),
      repositories: current.repositories.map((item) => item.repository.id === id ? change(item) : item),
    }))
  }

  return {
    ...data, apiOrigin, token, setToken,
    saveApiOrigin(origin: string) {
      if (origin === apiOrigin) return
      localStorage.setItem(originKey, origin)
      setApiOrigin(origin)
      setToken('')
    },
    saveProject(input: ProjectInput, id?: string) {
      if (data.projects.some((item) => item.slug === input.slug && item.id !== id)) return undefined
      const time = now()
      const projectId = id ?? crypto.randomUUID()
      setData((current) => ({ ...current, projects: id
        ? current.projects.map((item) => item.id === id ? { ...item, ...input, updated_at: time } : item)
        : [{ ...input, id: projectId, created_at: time, updated_at: time }, ...current.projects] }))
      return projectId
    },
    toggleProjectArchive(project: Project) {
      const time = now()
      setData((current) => ({ ...current, projects: current.projects.map((item) => item.id === project.id
        ? { ...item, status: item.status === 'active' ? 'archived' : 'active', updated_at: time } : item) }))
    },
    deleteProject(id: string) {
      const time = now()
      setData((current) => ({ ...current, searchVersion: current.searchVersion + 1,
        projects: current.projects.filter((item) => item.id !== id),
        repositories: current.repositories.filter(({ repository }) => repository.project_id !== id),
        documents: current.documents.filter((item) => item.project_id !== id),
        jobs: current.jobs.map((job) => job.payload.project_id === id && isActiveJob(job) ? cancel(job, time) : job),
      }))
    },
    associateRepository(projectId: string, input: RepositoryInput) {
      const canonical = input.source.replace(/\/+$/, '')
      const source = input.source_type === 'github' ? canonical.replace(/\.git$/, '') : canonical
      if (data.repositories.some(({ repository }) => repository.project_id === projectId
        && repository.canonical_source === source)) return false
      const time = now()
      const [, owner, repo] = source.match(/^https:\/\/github\.com\/([^/]+)\/([^/]+)$/i) ?? []
      const repository: Repository = { ...input, canonical_source: source, id: crypto.randomUUID(),
        project_id: projectId, default_branch: 'main', created_at: time, updated_at: time,
        ...(input.source_type === 'github' ? { remote_url: source, github_owner: owner, github_repo: repo,
          github_visibility: 'public' } : { local_path: source }) }
      setData((current) => ({ ...current, repositories: [...current.repositories, emptyRepository(repository)] }))
      return true
    },
    editRepository(id: string, input: Pick<Repository, 'name' | 'description' | 'role'>) {
      const time = now()
      updateRepository(id, (item) => ({ ...item, repository: { ...item.repository, ...input, updated_at: time } }), true)
    },
    refreshRepository(id: string) {
      const time = now()
      updateRepository(id, (item) => ({ ...item, repository: { ...item.repository, updated_at: time } }))
    },
    disassociateRepository(repository: Repository) {
      const time = now()
      setData((current) => ({ ...current, searchVersion: current.searchVersion + 1,
        repositories: current.repositories.filter((item) => item.repository.id !== repository.id),
        documents: current.documents.map((doc) => doc.repository_id === repository.id
          ? { ...doc, scope: 'project', repository_id: undefined, updated_at: time, version: doc.version + 1,
            origin: { repository_id: repository.id, repository_name: repository.name } } : doc),
        jobs: current.jobs.map((job) => job.payload.repository_id === repository.id && isActiveJob(job)
          ? cancel(job, time) : job),
      }))
    },
    syncFiles(repository: Repository) {
      const existing = data.jobs.find((job) => isActiveJob(job) && job.type === 'repository.sync'
        && job.payload.repository_id === repository.id)
      const job = existing ?? makeJob(repository, now())
      if (!existing) setData((current) => ({ ...current, jobs: [job, ...current.jobs] }))
      setSimulatedJobs((ids) => ids.includes(job.id) ? ids : [...ids, job.id])
    },
    cancelJob(id: string) {
      const time = now()
      setData((current) => ({ ...current, jobs: current.jobs.map((job) => job.id === id && isActiveJob(job)
        ? cancel(job, time) : job) }))
      setSimulatedJobs((ids) => ids.filter((jobId) => jobId !== id))
    },
    createDocument(projectId: string, draft: DocumentDraft, repositoryId?: string) {
      const time = now()
      const id = crypto.randomUUID()
      setData((current) => ({ ...current, searchVersion: current.searchVersion + 1,
        documents: [{ ...draft, id, project_id: projectId, repository_id: repositoryId,
          scope: repositoryId ? 'repository' : 'project', version: 1, content_bytes: byteLength(draft.content),
          created_at: time, updated_at: time }, ...current.documents] }))
      return id
    },
    saveDocument(id: string, draft: DocumentDraft, version: number) {
      const time = now()
      setData((current) => {
        if (!current.documents.some((doc) => doc.id === id && doc.version === version)) return current
        return { ...current, searchVersion: current.searchVersion + 1,
          documents: current.documents.map((doc) => doc.id === id
            ? { ...doc, ...draft, version: doc.version + 1, updated_at: time, content_bytes: byteLength(draft.content) } : doc) }
      })
    },
    simulateDocumentConflict(id: string, version: number) {
      setData((current) => ({ ...current, documents: current.documents.map((doc) => doc.id === id && doc.version === version
        ? { ...doc, version: doc.version + 1 } : doc) }))
    },
    deleteDocument(id: string, version: number) {
      setData((current) => {
        const remaining = current.documents.filter((doc) => doc.id !== id || doc.version !== version)
        return remaining.length === current.documents.length ? current
          : { ...current, documents: remaining, searchVersion: current.searchVersion + 1 }
      })
    },
    updatePolicy(id: string, policy: SyncPolicy) {
      updateRepository(id, (item) => ({ ...item,
        policies: [...item.policies.filter(({ kind }) => kind !== policy.kind), policy] }))
    },
  }
}

const ConsoleContext = createContext<ReturnType<typeof useConsoleStore> | null>(null)

export function MockConsoleProvider({ children }: { children: ReactNode }) {
  const store = useConsoleStore()
  return <ConsoleContext.Provider value={store}>{children}</ConsoleContext.Provider>
}

export function useMockConsole() {
  const store = useContext(ConsoleContext)
  if (!store) throw new Error('MockConsoleProvider is required')
  return store
}
