import { lazy, Suspense, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { ApiContent } from '@/api/ApiFeedback'
import { validBlameRange } from '@/api/endpoints'
import { useActiveSnapshot, useFileBlame, useFileContent, useFileHistory, useFileTree } from '@/api/queries'
import { RefSelector } from '@/components/code/RefSelector'
import { DataTable } from '@/components/shared/DataTable'
import { SectionRule } from '@/components/shared/SectionRule'
import { formatDate, repositoryPath, shortSha } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { EmptyState } from '@/components/ui/EmptyState'
import { Input } from '@/components/ui/Input'
import { Pagination } from '@/components/ui/Pagination'
import type { Repository } from '@/types/domain'

const CodeViewer = lazy(() => import('@/components/code/CodeViewer').then(({ CodeViewer }) => ({ default: CodeViewer })))

interface Folder {
  folders: Map<string, Folder>
  files: string[]
}

function tree(files: { path: string }[]): Folder {
  const root: Folder = { folders: new Map(), files: [] }
  for (const { path } of files) {
    let folder = root
    for (const segment of path.split('/').slice(0, -1)) {
      let child = folder.folders.get(segment)
      if (!child) {
        child = { folders: new Map(), files: [] }
        folder.folders.set(segment, child)
      }
      folder = child
    }
    folder.files.push(path)
  }
  return root
}

function fileSearch(path: string, commit: string) {
  const params = new URLSearchParams({ path })
  if (commit) params.set('commit', commit)
  return '?' + params
}

function FileTree({ folder, selected, commit }: { folder: Folder; selected: string; commit: string }) {
  return <ul className="file-tree">
    {[...folder.folders].map(([name, child]) => <li key={name}>
      <details open><summary>{name}/</summary><FileTree folder={child} selected={selected} commit={commit} /></details>
    </li>)}
    {folder.files.map((path) => <li key={path}>
      <Link to={fileSearch(path, commit)} aria-current={path === selected ? 'page' : undefined}>{path.split('/').at(-1)}</Link>
    </li>)}
  </ul>
}

interface FileContext { repo: Repository; path: string; ref?: string }
const annotationLimit = 20

function FileHistory({ repo, path, ref }: FileContext) {
  const [follow, setFollow] = useState(false)
  const [offset, setOffset] = useState(0)
  const query = useFileHistory(repo.project_id, repo.id, { path, ref, follow: follow || undefined, limit: annotationLimit, offset })
  const page = query.data
  const entries = page?.items ?? []

  return <SectionRule title="File history" actions={<label>
    <input type="checkbox" checked={follow} onChange={(event) => { setFollow(event.target.checked); setOffset(0) }} /> Follow renames
  </label>}>
    <ApiContent query={query} loading="Loading file history…">
      {page && <>
        <p className="metadata">
          {page.shallow && <span>Shallow history</span>}
          <span>{page.history_complete ? 'History complete' : 'History incomplete'}</span>
        </p>
        <DataTable label="File history" columns={['Commit', 'Change', 'Author', 'Date']} empty="No history for this file."
          rows={entries.map((entry) => ({ key: entry.hash, cells: [
            <Link className="text-link" to={fileSearch(entry.path, entry.hash)}>{entry.subject}</Link>,
            <div className="stacked-cell"><code>{entry.change_type}</code>
              {entry.old_path && <code>{entry.old_path} → {entry.path}</code>}</div>,
            entry.author_name, formatDate(entry.authored_at),
          ] }))} />
        <Pagination limit={page.limit} offset={page.offset} itemCount={entries.length} hasMore={page.has_more} onOffsetChange={setOffset} />
      </>}
    </ApiContent>
  </SectionRule>
}

function FileBlame({ repo, path, ref, highlightLine }: FileContext & { highlightLine: number }) {
  const [start, setStart] = useState('')
  const [end, setEnd] = useState('')
  const [range, setRange] = useState<{ start_line?: number; end_line?: number }>({})
  const [error, setError] = useState('')
  const query = useFileBlame(repo.project_id, repo.id, { path, ref, ...range })
  const blame = query.data
  const repoUrl = repositoryPath(repo.project_id, repo.id)

  return <SectionRule title="Blame">
    <form className="list-toolbar blame-range" noValidate onSubmit={(event) => {
      event.preventDefault()
      const first = start === '' ? undefined : Number(start)
      const last = end === '' ? undefined : Number(end)
      if (!validBlameRange(first, last)) {
        setError('Enter both line bounds for a range of at most 2,000 lines.')
        return
      }
      setError('')
      setRange({ start_line: first, end_line: last })
    }}>
      <div className="filters">
        <Input label="Start line" type="number" min={1} value={start} error={error || undefined} onChange={(event) => setStart(event.target.value)} />
        <Input label="End line" type="number" min={1} value={end} onChange={(event) => setEnd(event.target.value)} />
        <Button type="submit" variant="secondary">Apply range</Button>
      </div>
    </form>
    <ApiContent query={query} loading="Loading blame…">
      {blame && <>
        <p className="metadata">
          {blame.shallow && <span>Shallow history</span>}
          {blame.boundary_reached && <span>History boundary reached</span>}
          {blame.truncated && <span>Blame output was truncated. Use a line range to see more.</span>}
          {blame.lines.length > 0 && <span>Lines {blame.start_line}–{blame.end_line} of {blame.total_lines}</span>}
        </p>
        {blame.lines.length ? <div className="code-scroll" tabIndex={0} role="region" aria-label="Blame and source code">
          <ol className="code-lines">
            {blame.lines.map((line) => <li key={line.line} className={line.line === highlightLine ? 'selected-line' : undefined}>
              <span className="line-number">{line.line}</span>
              <Link className="blame-label" title={line.summary} to={repoUrl + '/commits?sha=' + encodeURIComponent(line.commit_hash)}>
                {shortSha(line.commit_hash)} {line.author_name}
              </Link>
              <code>{line.content || '\u00a0'}</code>
            </li>)}
          </ol>
        </div> : <p className="rule-message">No blame information for this file.</p>}
      </>}
    </ApiContent>
  </SectionRule>
}

function FileViewer({ repo, path, commit, activeCommit, highlightLine }: {
  repo: Repository; path: string; commit: string; activeCommit?: string; highlightLine: number
}) {
  const fileQuery = useFileContent(repo.project_id, repo.id, commit, path)
  const file = fileQuery.data
  const [overlay, setOverlay] = useState<'history' | 'blame'>()
  const ref = commit || activeCommit

  return <section className="code-pane" aria-label="Read-only code viewer">
    <ApiContent query={fileQuery} loading="Loading file content…">
      {file && <>
        <div className="section-heading">
          <div className="stacked-cell">
            <h2><code>{file.path}</code></h2>
            <div className="metadata">
              <span>{'size_bytes' in file ? file.size_bytes : file.size} bytes</span>
              <span>{file.line_count} lines</span>
              {file.sha256 && <span title={file.sha256}>SHA-256 <code>{shortSha(file.sha256)}</code></span>}
              {file.is_executable && <span>Executable <code>{file.mode}</code></span>}
            </div>
          </div>
          <div className="inline-actions">
            <Button size="sm" variant="secondary" aria-pressed={overlay === 'history'}
              onClick={() => setOverlay(overlay === 'history' ? undefined : 'history')}>File history</Button>
            <Button size="sm" variant="secondary" aria-pressed={overlay === 'blame'} disabled={file.binary}
              onClick={() => setOverlay(overlay === 'blame' ? undefined : 'blame')}>Blame</Button>
          </div>
        </div>
        {overlay === 'history' && <FileHistory repo={repo} path={path} ref={ref} />}
        {file.binary ? <p className="rule-message">Binary file. Text preview is unavailable.</p>
          : overlay === 'blame' ? <FileBlame repo={repo} path={path} ref={ref} highlightLine={highlightLine} />
          : <Suspense fallback={<p className="rule-message" role="status">Loading code viewer…</p>}>
            <CodeViewer path={file.path} content={file.content ?? ''} highlightLine={highlightLine} />
          </Suspense>}
      </>}
    </ApiContent>
  </section>
}

const treeLimit = 30

export function RepositoryFiles({ repo }: { repo: Repository }) {
  const [params, setParams] = useSearchParams()
  const commit = params.get('commit') ?? ''

  return <RepositoryFileBrowser key={commit} repo={repo} commit={commit}
    selectedPath={params.get('path')} highlightLine={Number(params.get('line'))}
    onCommit={(sha) => setParams(sha ? { commit: sha } : {})} />
}

function RepositoryFileBrowser({ repo, commit, selectedPath, highlightLine, onCommit }: {
  repo: Repository; commit: string; selectedPath: string | null; highlightLine: number; onCommit: (sha: string) => void
}) {
  const [q, setQuery] = useState('')
  const [prefix, setPrefix] = useState('')
  const [filters, setFilters] = useState({ q: '', path_prefix: '', offset: 0 })
  const repoUrl = repositoryPath(repo.project_id, repo.id)
  const active = useActiveSnapshot(repo.project_id, repo.id, !commit)
  const listing = useFileTree(repo.project_id, repo.id, commit, { ...filters, limit: treeLimit })
  const page = listing.data
  const path = selectedPath || page?.items?.[0]?.path || ''

  return <>
    <div className="list-toolbar">
      <div className="metadata">
        {commit ? <>
          <strong>Historical commit</strong> <code className="historical-sha">{commit}</code>
          <Link className="text-link" to={repoUrl + '/files'}>Return to active snapshot</Link>
        </> : <>
          <strong>Active snapshot</strong>
          {active.data && <>
            <code>v{active.data.version}</code>
            {active.data.resolved_ref && <code>{active.data.resolved_ref}</code>}
          </>}
        </>}
      </div>
      <RefSelector projectId={repo.project_id} repoId={repo.id} placeholder="Browse a branch or tag"
        onSelect={(ref) => { if (ref) onCommit(ref.commit_hash || ref.object_hash) }} />
    </div>
    {!commit && <ApiContent query={active} loading="Checking the active snapshot…">{null}</ApiContent>}
    {!commit && active.data === null ? <EmptyState title="No active snapshot"
      description="Publish a snapshot to browse indexed files."
      action={<Link className="text-link" to={repoUrl + '/snapshots'}>Publish snapshot</Link>} />
      : <div className="file-browser">
        <aside aria-label="File tree" className="tree-pane">
          <h2>File tree</h2>
          <form className="tree-filters" onSubmit={(event) => {
            event.preventDefault()
            setFilters({ q, path_prefix: prefix, offset: 0 })
          }}>
            {!commit && <Input label="Search files" value={q} onChange={(event) => setQuery(event.target.value)} />}
            <Input label="Path prefix" value={prefix} onChange={(event) => setPrefix(event.target.value)} />
            <Button type="submit" size="sm" variant="secondary">Filter files</Button>
          </form>
          <ApiContent query={listing} loading="Loading file tree…">
            {page && <>
              {page.items?.length ? <FileTree folder={tree(page.items)} selected={path} commit={commit} />
                : <p className="rule-message">No files match this page.</p>}
              <Pagination limit={page.limit} offset={page.offset} total={page.total}
                onOffsetChange={(offset) => setFilters({ ...filters, offset })} />
            </>}
          </ApiContent>
        </aside>
        {path ? <FileViewer key={path} repo={repo} path={path} commit={commit} activeCommit={active.data?.commit_hash} highlightLine={highlightLine} />
          : <p className="rule-message">Choose a file in the tree.</p>}
      </div>}
  </>
}
