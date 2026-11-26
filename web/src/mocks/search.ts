import type { Repository, SearchHit, SearchResult, SearchSourceType } from '@/types/domain'
import type { ConsoleData } from './MockConsoleProvider'

export type ScopeFilters =
  | { scope: 'project' | 'all' }
  | { scope: 'repository'; repository_id: string }
  | { scope: 'cross_repository'; repository_ids: string[]; repository_role?: Repository['role']; include_project_documents?: true }

export type SearchFilters = ScopeFilters & {
  q: string
  source_type: SearchSourceType[]
  path_prefix: string
}

function escapeSnippet(text: string) {
  return text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}

export function mockSearch(data: ConsoleData, projectId: string, filters: SearchFilters, offset: number, limit: number,
  cacheStatus: SearchResult['cache_status']): SearchResult {
  const matcher = new RegExp(filters.q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'i')
  const matches: SearchHit[] = []

  function addHit(hit: Omit<SearchHit, 'snippet' | 'score' | 'project_id'>, content: string) {
    if (filters.source_type.length && !filters.source_type.includes(hit.source_type)) return
    if (filters.path_prefix && !hit.location.path?.startsWith(filters.path_prefix)) return
    const lines = content.split('\n')
    const lineIndex = lines.findIndex((line) => matcher.test(line))
    const text = lineIndex >= 0 ? lines[lineIndex] : hit.title
    const match = matcher.exec(text)
    if (!match) return
    const { index } = match
    const snippet = escapeSnippet(text.slice(Math.max(0, index - 55), index))
      + '<mark>' + escapeSnippet(match[0]) + '</mark>'
      + escapeSnippet(text.slice(index + match[0].length, index + match[0].length + 95))
    matches.push({ ...hit, project_id: projectId, snippet, score: matcher.test(hit.title) ? 2 : 1,
      location: { ...hit.location, line: lineIndex < 0 ? undefined : lineIndex + 1 } })
  }

  const repositories = data.repositories.filter(({ repository }) => {
    if (repository.project_id !== projectId) return false
    switch (filters.scope) {
      case 'project': return false
      case 'repository': return repository.id === filters.repository_id
      case 'cross_repository': return (!filters.repository_ids.length || filters.repository_ids.includes(repository.id))
        && (!filters.repository_role || repository.role === filters.repository_role)
      case 'all': return true
    }
  })
  const repoMap = new Map(repositories.map(({ repository }) => [repository.id, repository]))
  const apiPath = '/api/v1/projects/' + encodeURIComponent(projectId)
  for (const { repository, files, snapshots } of repositories) {
    const active = snapshots.find((item) => item.status === 'active')
    for (const file of files.filter((file) => file.snapshot_id === active?.id)) {
      addHit({ kind: 'source_file', source_type: repository.source_type, id: file.id, title: file.path,
        updated_at: active?.updated_at ?? file.created_at, location: {
          scope: 'repository', repository_id: repository.id, repository_name: repository.name, repository_role: repository.role,
          repository_source: repository.source_type, snapshot_id: file.snapshot_id, snapshot_version: active?.version,
          commit_hash: active?.commit_hash, path: file.path,
          api_path: apiPath + '/repositories/' + encodeURIComponent(repository.id) + '/files/content?path=' + encodeURIComponent(file.path),
        } }, file.content ?? '')
    }
  }
  const includeProject = filters.scope === 'project' || filters.scope === 'all'
    || (filters.scope === 'cross_repository' && filters.include_project_documents)
  for (const doc of data.documents) {
    if (doc.project_id !== projectId) continue
    const repository = doc.repository_id ? repoMap.get(doc.repository_id) : undefined
    if (doc.scope === 'project' ? !includeProject : !repository) continue
    addHit({ kind: 'document', source_type: 'document', id: doc.id, title: doc.title, updated_at: doc.updated_at,
      location: { scope: doc.scope, repository_id: doc.repository_id, repository_name: repository?.name,
        repository_role: repository?.role, document_id: doc.id, document_title: doc.title, document_version: doc.version,
        api_path: apiPath + (repository ? '/repositories/' + encodeURIComponent(repository.id) : '') + '/documents/' + encodeURIComponent(doc.id),
      } }, doc.content)
  }
  matches.sort((a, b) => b.score - a.score || b.updated_at.localeCompare(a.updated_at))
  return { query: filters.q, total: matches.length, items: matches.slice(offset, offset + limit), limit, offset,
    search_version: data.searchVersion, cache_status: cacheStatus }
}
