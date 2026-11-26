import { useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { ApiContent, ApiFailure } from '@/api/ApiFeedback'
import { projectSearchParams, searchScopes, type SearchFilters } from '@/api/endpoints'
import { useProjectSearch, useRepositories } from '@/api/queries'
import { SafeSnippet } from '@/components/shared/SafeSnippet'
import { PageHeading } from '@/components/shared/SectionRule'
import { documentPath, formatDate, repositoryPath, shortSha } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Pagination } from '@/components/ui/Pagination'
import { Select } from '@/components/ui/Select'
import { StatusBadge } from '@/components/ui/StatusBadge'
import type { RepositoryRole, SearchHit, SearchResult, SearchScope, SearchSourceType } from '@/types/domain'
import { useProject } from './workspace'

const limit = 20
const allSources: { value: SearchSourceType; label: string }[] = [
  { value: 'local_git', label: 'Local Git' }, { value: 'github', label: 'GitHub' }, { value: 'document', label: 'Documents' },
]

function searchDraft(params: URLSearchParams) {
  const scope = params.get('scope') as SearchScope
  const cross = scope === 'cross_repository'
  const role = params.get('repository_role')
  const repositoryRole: RepositoryRole | '' = cross && (role === 'primary' || role === 'reference') ? role : ''
  const values = (key: string) => params.getAll(key).flatMap((value) => value.split(',').map((part) => part.trim())).filter(Boolean)
  const selectedSources = values('source_type')
  return {
    q: params.get('q') ?? '',
    scope: searchScopes.includes(scope) ? scope : '' as const,
    repository_id: scope === 'repository' ? params.get('repository_id') ?? '' : '',
    repository_ids: cross ? values('repository_ids') : [],
    repository_role: repositoryRole,
    include_project_documents: cross && params.get('include_project_documents') === 'true',
    source_type: allSources.map(({ value }) => value)
      .filter((value) => selectedSources.includes(value) && (scope !== 'project' || value === 'document')),
    path_prefix: scope === 'project' ? '' : params.get('path_prefix') ?? '',
  }
}

type SearchDraft = ReturnType<typeof searchDraft>

function validFilters(draft: SearchDraft, offset: number): SearchFilters | undefined {
  if (!draft.q.trim() || !draft.scope || (draft.scope === 'repository' && !draft.repository_id)) return
  return { ...draft, scope: draft.scope, repository_role: draft.repository_role || undefined, limit, offset }
}

function searchUrl(filters: SearchFilters) {
  const params = new URLSearchParams()
  for (const [name, value] of Object.entries(projectSearchParams(filters))) {
    if (name !== 'limit' && value !== undefined && value !== '') params.set(name, String(value))
  }
  return params
}

function SearchHitRow({ projectId, hit }: { projectId: string; hit: SearchHit }) {
  const { document_id, document_version, repository_id, repository_name, repository_role, path, line, url,
    snapshot_version, commit_hash } = hit.location
  const file = hit.kind === 'source_file'
  const fileLine = file && line && line > 0 ? line : undefined
  let link: string | undefined
  if (file && repository_id && path) {
    const params = new URLSearchParams({ path })
    if (fileLine) params.set('line', String(fileLine))
    link = repositoryPath(projectId, repository_id) + '/files?' + params + (fileLine ? '#L' + fileLine : '')
  } else if (!file && document_id) {
    link = documentPath(projectId, document_id)
  }
  const title = hit.title + (fileLine ? ':' + fileLine : '')
  const external = url?.startsWith('https://') && URL.canParse(url) ? url : undefined

  return <li>
    <h3>{link ? <Link className="text-link" to={link}>{title}</Link> : title}</h3>
    <div className="metadata">
      <StatusBadge status={hit.source_type === 'document' ? 'Document' : hit.source_type} />
      {repository_role && <StatusBadge status={repository_role} />}
      <span>{repository_name ?? (repository_id ? 'Repository' : 'Project documents')}</span>
      {file && commit_hash && <code title={commit_hash}>{shortSha(commit_hash)}</code>}
      {file && snapshot_version !== undefined && <span>Snapshot <code>v{snapshot_version}</code></span>}
      {!file && document_version !== undefined && <code>v{document_version}</code>}
      <span>Score {hit.score.toLocaleString('en', { maximumFractionDigits: 3 })}</span>
      <span>Updated {formatDate(hit.updated_at)}</span>
    </div>
    <p className={file ? 'code-snippet' : undefined}><SafeSnippet snippet={hit.snippet} /></p>
    {external && <div><a className="text-link search-external" href={external} target="_blank" rel="noreferrer">View on GitHub</a></div>}
  </li>
}

function SearchResults({ projectId, result, onOffsetChange }: {
  projectId: string; result: SearchResult; onOffsetChange: (offset: number) => void
}) {
  return <section className="section-rule" aria-label="Search results">
    <div className="section-heading">
      <h2>Results <span className="muted">({result.total})</span></h2>
      <div className="metadata search-diagnostics">
        {result.cache_status && <span><code>X-Search-Cache</code> {result.cache_status}</span>}
        <span><code>X-Search-Version</code> {result.search_version}</span>
      </div>
    </div>
    {result.items.length ? <ul className="result-list">{result.items.map((hit) =>
      <SearchHitRow key={hit.kind + ':' + hit.id} projectId={projectId} hit={hit} />)}
    </ul> : <p className="rule-message">No matches. Try a different query or broaden the scope.</p>}
    <Pagination total={result.total} limit={result.limit} offset={result.offset} onOffsetChange={onOffsetChange} />
  </section>
}

function SearchWorkspace({ projectId, params, setParams }: {
  projectId: string; params: URLSearchParams; setParams: (params: URLSearchParams) => void
}) {
  const repositoryQuery = useRepositories(projectId, { limit: 100 })
  const repositories = repositoryQuery.data?.items ?? []
  const [draft, setDraft] = useState(() => searchDraft(params))
  const [scopeChanged, setScopeChanged] = useState(false)
  const [error, setError] = useState('')
  const rawOffset = Number(params.get('offset'))
  const offset = Number.isSafeInteger(rawOffset) && rawOffset >= 0 ? rawOffset : 0
  const submitted = validFilters(searchDraft(params), offset)
  const search = useProjectSearch(projectId, submitted)
  const { scope, repository_id, repository_ids, repository_role, include_project_documents, source_type, path_prefix } = draft
  const cross = scope === 'cross_repository'
  const single = scope === 'repository'
  const projectOnly = scope === 'project'
  const sources = projectOnly ? allSources.filter(({ value }) => value === 'document') : allSources

  function update(fields: Partial<SearchDraft>) {
    setDraft((current) => ({ ...current, ...fields }))
  }

  function changeScope(value: SearchScope | '') {
    const compatibleSources = value === 'project' ? source_type.filter((type) => type === 'document') : source_type
    update({ scope: value, repository_id: '', repository_ids: [], repository_role: '', include_project_documents: false,
      source_type: compatibleSources, path_prefix: value === 'project' ? '' : path_prefix })
    setScopeChanged(true)
    setError('')
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const filters = validFilters(draft, 0)
    if (!filters) return setError(!draft.q.trim() ? 'Enter a search query.' : !scope ? 'Choose a search scope.' : 'Choose a repository.')
    setError('')
    setScopeChanged(false)
    const next = searchUrl(filters)
    if (next.toString() === params.toString()) void search.refetch()
    else setParams(next)
  }

  return <>
    <form className="search-form" onSubmit={submit}>
      <div className="search-line">
        <Input type="search" label="Query" name="q" maxLength={200} placeholder="Search code and documents" required
          value={draft.q} onChange={(event) => update({ q: event.target.value })} />
        <Select label="Search scope" required value={scope}
          onChange={(event) => changeScope(event.target.value as SearchScope | '')}>
          <option value="">Select a scope</option><option value="project">Project documents</option>
          <option value="repository">Single repository</option><option value="cross_repository">Cross-repository</option>
          <option value="all">All project knowledge</option>
        </Select>
        <Button type="submit">Search</Button>
      </div>
      {scope && <div className="search-options">
        {(cross || single) && <>
          {repositoryQuery.isPending && <p className="muted" role="status">Loading repositories…</p>}
          {repositoryQuery.error && <ApiFailure error={repositoryQuery.error} retry={() => { void repositoryQuery.refetch() }} />}
          <div className="filters">
            {single && <Select label="Repository" required value={repository_id}
              onChange={(event) => update({ repository_id: event.target.value })}>
              <option value="">Select a repository</option>
              {repository_id && !repositories.some(({ id }) => id === repository_id) &&
                <option value={repository_id}>{repository_id}</option>}
              {repositories.map((repo) => <option value={repo.id} key={repo.id}>{repo.name}</option>)}
            </Select>}
            {cross && <>
              <fieldset className="check-group"><legend>Repositories</legend>
                {repositories.length ? repositories.map((repo) => <label key={repo.id}>
                  <input type="checkbox" checked={repository_ids.includes(repo.id)} onChange={(event) => update({
                    repository_ids: event.target.checked ? [...repository_ids, repo.id] : repository_ids.filter((id) => id !== repo.id),
                  })} />{repo.name}
                </label>) : !repositoryQuery.isPending && !repositoryQuery.error &&
                  <p className="muted">Associate repositories to search their content.</p>}
              </fieldset>
              <Select label="Repository role" value={repository_role} onChange={(event) =>
                update({ repository_role: event.target.value as RepositoryRole | '' })}>
                <option value="">All roles</option><option value="primary">Primary</option><option value="reference">Reference</option>
              </Select>
            </>}
          </div>
        </>}
        {cross && <label className="check"><input type="checkbox" checked={include_project_documents}
          onChange={(event) => update({ include_project_documents: event.target.checked })} />Include project documents</label>}
        <div className="filters search-source-filters">
          <fieldset className="check-group"><legend>Source type</legend>
            {sources.map(({ value, label }) => <label key={value}><input type="checkbox" checked={source_type.includes(value)}
              onChange={(event) => update({
                source_type: event.target.checked ? [...source_type, value] : source_type.filter((type) => type !== value),
              })} />{label}</label>)}
          </fieldset>
          {!projectOnly && <Input label="Path prefix" value={path_prefix} placeholder="internal/"
            onChange={(event) => update({ path_prefix: event.target.value })} />}
        </div>
      </div>}
      {error && <p role="alert" className="field-error">{error}</p>}
    </form>
    {submitted && !scopeChanged && <ApiContent query={search} loading="Searching…">
      {search.data && <SearchResults projectId={projectId} result={search.data}
        onOffsetChange={(nextOffset) => setParams(searchUrl({ ...submitted, offset: nextOffset }))} />}
    </ApiContent>}
  </>
}

export function SearchPage() {
  const project = useProject()
  const [params, setParams] = useSearchParams()
  return <>
    <PageHeading title="Search" description="Search Markdown documents and files in active repository snapshots." />
    <SearchWorkspace key={project.id + '?' + params.toString()} projectId={project.id} params={params} setParams={setParams} />
  </>
}
