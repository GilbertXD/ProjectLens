import { useState } from 'react'
import type { FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { SafeSnippet } from '@/components/shared/SafeSnippet'
import { PageHeading } from '@/components/shared/SectionRule'
import { documentPath, repositoryPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Pagination } from '@/components/ui/Pagination'
import { Select } from '@/components/ui/Select'
import { StatusBadge } from '@/components/ui/StatusBadge'
import { useMockConsole } from '@/mocks/MockConsoleProvider'
import { mockSearch } from '@/mocks/search'
import type { ScopeFilters, SearchFilters } from '@/mocks/search'
import type { RepositoryRole, SearchScope, SearchSourceType } from '@/types/domain'
import { useProject } from './workspace'

const limit = 20
const allSources: { value: SearchSourceType; label: string }[] = [
  { value: 'local_git', label: 'Local Git' }, { value: 'github', label: 'GitHub' }, { value: 'document', label: 'Documents' },
]

export function SearchPage() {
  const project = useProject()
  const store = useMockConsole()
  const repositories = store.repositories.map(({ repository }) => repository).filter((repo) => repo.project_id === project.id)
  const [scope, setScope] = useState<SearchScope | ''>('')
  const [repositoryId, setRepositoryId] = useState('')
  const [repositoryIds, setRepositoryIds] = useState<string[]>([])
  const [role, setRole] = useState<RepositoryRole | ''>('')
  const [includeProject, setIncludeProject] = useState(false)
  const [sourceTypes, setSourceTypes] = useState<SearchSourceType[]>([])
  const [prefix, setPrefix] = useState('')
  const [submitted, setSubmitted] = useState<SearchFilters>()
  const [cached, setCached] = useState<{ key: string; version: number; hit: boolean }>()
  const [offset, setOffset] = useState(0)
  const [error, setError] = useState('')
  const cross = scope === 'cross_repository'
  const single = scope === 'repository'
  const projectOnly = scope === 'project'
  const sources = projectOnly ? allSources.filter(({ value }) => value === 'document') : allSources
  const result = submitted ? mockSearch(store, project.id, submitted, offset, limit,
    cached?.hit && cached.version === store.searchVersion ? 'hit' : 'miss') : undefined

  function changeScope(value: SearchScope | '') {
    setScope(value)
    setRepositoryId('')
    setRepositoryIds([])
    setRole('')
    setIncludeProject(false)
    setSourceTypes([])
    setPrefix('')
    setSubmitted(undefined)
    setError('')
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const q = String(new FormData(event.currentTarget).get('q')).trim()
    if (!q) return setError('Enter a search query.')
    if (!scope) return setError('Choose a search scope.')
    if (single && !repositoryId) return setError('Choose a repository.')
    const ownership: ScopeFilters = scope === 'repository' ? { scope, repository_id: repositoryId }
      : scope === 'cross_repository' ? { scope, repository_ids: repositoryIds, repository_role: role || undefined,
        include_project_documents: includeProject || undefined } : { scope }
    const filters = { ...ownership, q, source_type: sourceTypes, path_prefix: projectOnly ? '' : prefix.trim() }
    const key = JSON.stringify(filters)
    setCached({ key, version: store.searchVersion, hit: cached?.key === key && cached.version === store.searchVersion })
    setSubmitted(filters)
    setOffset(0)
    setError('')
  }

  return <>
    <PageHeading title="Search" description="Search Markdown documents and files in active repository snapshots." />
    <form className="search-form" onSubmit={submit}>
      <div className="search-line">
        <Input type="search" label="Query" name="q" maxLength={200} placeholder="Search code and documents" required />
        <Select label="Search scope" required value={scope}
          onChange={(event) => changeScope(event.target.value as SearchScope | '')}>
          <option value="">Select a scope</option><option value="project">Project documents</option>
          <option value="repository">Single repository</option><option value="cross_repository">Cross-repository</option>
          <option value="all">All project knowledge</option>
        </Select>
        <Button type="submit">Search</Button>
      </div>
      {scope && <div className="search-options">
        {(cross || single) && <div className="filters">
          {single && <Select label="Repository" required value={repositoryId} onChange={(event) => setRepositoryId(event.target.value)}>
            <option value="">Select a repository</option>
            {repositories.map((repo) => <option value={repo.id} key={repo.id}>{repo.name}</option>)}
          </Select>}
          {cross && <>
            <fieldset className="check-group"><legend>Repositories</legend>
              {repositories.length ? repositories.map((repo) => <label key={repo.id}>
                <input type="checkbox" checked={repositoryIds.includes(repo.id)} onChange={(event) =>
                  setRepositoryIds(event.target.checked ? [...repositoryIds, repo.id] : repositoryIds.filter((id) => id !== repo.id))} />
                {repo.name}
              </label>) : <p className="muted">Associate repositories to search their content.</p>}
            </fieldset>
            <Select label="Repository role" value={role} onChange={(event) => setRole(event.target.value as RepositoryRole | '')}>
              <option value="">All roles</option><option value="primary">Primary</option><option value="reference">Reference</option>
            </Select>
          </>}
        </div>}
        {cross && <label className="check"><input type="checkbox" checked={includeProject}
          onChange={(event) => setIncludeProject(event.target.checked)} />Include project documents</label>}
        <div className="filters">
          <fieldset className="check-group"><legend>Source type</legend>
            {sources.map(({ value, label }) => <label key={value}><input type="checkbox" checked={sourceTypes.includes(value)}
              onChange={(event) => setSourceTypes(event.target.checked ? [...sourceTypes, value] : sourceTypes.filter((type) => type !== value))} />
              {label}</label>)}
          </fieldset>
          {!projectOnly && <Input label="Path prefix" value={prefix} placeholder="internal/" onChange={(event) => setPrefix(event.target.value)} />}
        </div>
      </div>}
      {error && <p role="alert" className="field-error">{error}</p>}
    </form>
    {result && <section className="section-rule" aria-label="Search results">
      <div className="section-heading">
        <h2>Results <span className="muted">({result.total})</span></h2>
        <div className="metadata search-diagnostics"><span><code>X-Search-Cache</code> {result.cache_status}</span>
          <span><code>X-Search-Version</code> {result.search_version}</span></div>
      </div>
      {result.items.length ? <ul className="result-list">{result.items.map((hit) => {
        const { document_id, repository_id, path, line, repository_name } = hit.location
        const link = hit.kind === 'document' && document_id ? documentPath(project.id, document_id)
          : repositoryPath(project.id, repository_id!) + '/files?' + new URLSearchParams({
            path: path ?? '', ...(line ? { line: String(line) } : {}),
          }) + (line ? '#L' + line : '')
        return <li key={hit.id}>
          <h3><Link className="text-link" to={link}>{hit.title}{hit.kind === 'source_file' && line ? ':' + line : ''}</Link></h3>
          <div className="metadata"><StatusBadge status={hit.source_type === 'document' ? 'Document' : hit.source_type} />
            <span>{repository_name ?? 'Project documents'}</span></div>
          <p className={hit.kind === 'source_file' ? 'code-snippet' : undefined}><SafeSnippet snippet={hit.snippet} /></p>
        </li>
      })}</ul> : <p className="rule-message">No matches. Try a different query or broaden the scope.</p>}
      <Pagination total={result.total} limit={limit} offset={offset} onOffsetChange={setOffset} />
    </section>}
  </>
}
