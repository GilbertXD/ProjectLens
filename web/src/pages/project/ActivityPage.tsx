import { useState } from 'react'
import { Link } from 'react-router-dom'
import { DataTable } from '@/components/shared/DataTable'
import { SafeMarkdown } from '@/components/shared/SafeMarkdown'
import { PageHeading, SectionRule } from '@/components/shared/SectionRule'
import { formatDate, repositoryPath, shortSha } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Pagination } from '@/components/ui/Pagination'
import { Select } from '@/components/ui/Select'
import { StatusBadge } from '@/components/ui/StatusBadge'
import { useMockConsole } from '@/mocks/MockConsoleProvider'
import { useProject } from './workspace'

const views = { commits: 'Commits', issue: 'Issues', pull_request: 'Pull requests' }
type ActivityView = keyof typeof views
const limit = 2

export function ActivityPage() {
  const project = useProject()
  const store = useMockConsole()
  const [view, setView] = useState<ActivityView>('commits')
  const [repoId, setRepoId] = useState('')
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState<string>()
  const repositories = store.repositories.filter(({ repository }) => repository.project_id === project.id)
  const filtered = repositories.filter(({ repository }) => !repoId || repository.id === repoId)
  const commits = filtered.flatMap(({ repository, history }) =>
    (history?.items ?? []).map((commit) => ({ repository, commit })))
    .sort((a, b) => b.commit.committed_at.localeCompare(a.commit.committed_at))
  const threads = filtered.flatMap(({ repository, activity }) => activity
    .filter(({ item }) => item.kind === view)
    .map((thread) => ({ ...thread, repository, key: repository.id + '/' + thread.item.number })))
    .sort((a, b) => b.item.updated_at.localeCompare(a.item.updated_at) || (b.item.number ?? 0) - (a.item.number ?? 0))
  const page = threads.slice(offset, offset + limit)
  const detail = page.find((thread) => thread.key === selected)

  function resetPage() {
    setOffset(0)
    setSelected(undefined)
  }

  return <>
    <PageHeading title="Activity" description="Browse imported Git history and GitHub discussions." />
    <div className="list-toolbar">
      <div className="subnav" role="group" aria-label="Activity view">
        {(Object.entries(views) as [ActivityView, string][]).map(([value, label]) => <button key={value} type="button"
          aria-pressed={view === value} onClick={() => { setView(value); resetPage() }}>{label}</button>)}
      </div>
      <Select label="Repository" value={repoId} onChange={(event) => { setRepoId(event.target.value); resetPage() }}>
        <option value="">All repositories</option>
        {repositories.map(({ repository }) => <option value={repository.id} key={repository.id}>{repository.name}</option>)}
      </Select>
    </div>
    {view === 'commits' ? <>
      <DataTable label="Commits" columns={['Commit', 'Repository', 'Author', 'Committed']}
        empty="No imported commits for this selection."
        rows={commits.slice(offset, offset + limit).map(({ repository, commit }) => ({
          key: repository.id + commit.hash, cells: [
            <div className="stacked-cell">
              <Link className="text-link" to={repositoryPath(project.id, repository.id) + '/commits/' + commit.hash}>{commit.subject}</Link>
              <code>{shortSha(commit.hash)}</code>
            </div>, repository.name, commit.author_name, formatDate(commit.committed_at),
          ],
        }))} />
      <Pagination limit={limit} offset={offset} total={commits.length} onOffsetChange={setOffset} />
    </> : <>
      <div className={detail ? 'split-pane activity-pane' : undefined}>
        <div>
          <DataTable label={views[view]} columns={['Title', 'Repository', 'State', 'Updated']}
            empty={offset ? 'No more entries. Return to the previous page.' : 'No imported GitHub activity for this selection.'}
            rows={page.map(({ repository, item, key }) => ({ key, cells: [
              <button type="button" className="text-link" aria-pressed={selected === key}
                onClick={() => setSelected(selected === key ? undefined : key)}>#{item.number} {item.title}</button>,
              repository.name, <StatusBadge status={item.state === 'open' ? 'Open' : item.state === 'closed' ? 'Closed' : item.state ?? '—'} />,
              formatDate(item.updated_at),
            ] }))} />
          <Pagination limit={limit} offset={offset} itemCount={page.length} hasMore={page.length === limit}
            onOffsetChange={(next) => { setOffset(next); setSelected(undefined) }} />
        </div>
        {detail && <SectionRule title={detail.item.title ?? 'Discussion'} actions={<Button variant="ghost" size="sm"
          onClick={() => setSelected(undefined)}>Close details</Button>}>
          <p className="metadata"><span>#{detail.item.number}</span><span>{detail.item.user?.login}</span><span>{formatDate(detail.item.created_at)}</span></p>
          <SafeMarkdown content={detail.item.body ?? ''} />
          <SectionRule title="Comments">
            {detail.comments.length ? <ul className="result-list">{detail.comments.map((comment) => <li key={comment.id}>
              <p className="metadata"><strong>{comment.user?.login}</strong><span>{formatDate(comment.created_at)}</span></p>
              <SafeMarkdown content={comment.body ?? ''} />
            </li>)}</ul> : <p className="rule-message">No imported comments.</p>}
          </SectionRule>
          {view === 'pull_request' && <SectionRule title="Pull request commits">
            {detail.commits.length ? <ul className="result-list">{detail.commits.map((commit) => <li key={commit.hash}>
              <Link className="text-link" to={repositoryPath(project.id, detail.repository.id) + '/commits/' + commit.hash}>
                <code>{shortSha(commit.hash)}</code> {commit.subject}
              </Link>
            </li>)}</ul> : <p className="rule-message">No imported pull request commits.</p>}
          </SectionRule>}
        </SectionRule>}
      </div>
    </>}
  </>
}
