import { useState } from 'react'
import { Link } from 'react-router-dom'
import { ApiContent } from '@/api/ApiFeedback'
import { RepositorySyncAction } from '@/api/RepositorySyncAction'
import { useActivity, useCommits, useDiscussion, usePullCommits, useRepositories } from '@/api/queries'
import { RefSelector } from '@/components/code/RefSelector'
import { DataTable } from '@/components/shared/DataTable'
import { SafeMarkdown } from '@/components/shared/SafeMarkdown'
import { PageHeading, SectionRule } from '@/components/shared/SectionRule'
import { formatDate, repositoryPath, shortSha } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Pagination } from '@/components/ui/Pagination'
import { Select } from '@/components/ui/Select'
import { StatusBadge } from '@/components/ui/StatusBadge'
import type { Repository } from '@/types/domain'
import { useProject } from './workspace'

const views = { commits: 'Commits', issue: 'Issues', pull: 'Pull requests' } as const
type ActivityView = keyof typeof views
type DiscussionKind = Exclude<ActivityView, 'commits'>
interface DiscussionTarget { repoId: string; number: number }
const listLimit = 20
const detailLimit = 20

function commitUrl(projectId: string, repoId: string, sha: string) {
  return repositoryPath(projectId, repoId) + '/commits?sha=' + encodeURIComponent(sha)
}

function CommitActivity({ repo }: { repo: Repository }) {
  const [ref, setRef] = useState('')
  const [offset, setOffset] = useState(0)
  const query = useCommits(repo.project_id, repo.id, { ref, limit: detailLimit, offset })
  const history = query.data

  return <>
    <div className="list-toolbar">
      <RefSelector projectId={repo.project_id} repoId={repo.id} value={ref}
        onSelect={(next) => { setRef(next?.ref ?? ''); setOffset(0) }} />
    </div>
    <ApiContent query={query} loading="Loading commits…">
      <DataTable label="Commits" columns={['Commit', 'Repository', 'Author', 'Committed']}
        empty="No imported commits for this ref."
        rows={(history?.items ?? []).map((commit) => ({ key: commit.hash, cells: [
          <div className="stacked-cell">
            <Link className="text-link" to={commitUrl(repo.project_id, repo.id, commit.hash)}>{commit.subject}</Link>
            <code>{shortSha(commit.hash)}</code>
          </div>, repo.name, commit.author_name, formatDate(commit.committed_at),
        ] }))} />
      {history && <Pagination limit={detailLimit} offset={offset} total={history.total} onOffsetChange={setOffset} />}
    </ApiContent>
  </>
}

function PullCommits({ projectId, repoId, number }: DiscussionTarget & { projectId: string }) {
  const [offset, setOffset] = useState(0)
  const query = usePullCommits(projectId, repoId, number, { limit: detailLimit, offset })
  const page = query.data

  return <SectionRule title="Pull request commits">
    <ApiContent query={query} loading="Loading pull request commits…">
      {page && <>
        {page.items.length ? <ul className="result-list">{page.items.map((commit, index) => {
          const sha = commit.sha || commit.commit_id
          return <li key={sha ?? commit.id ?? index}>
            {sha ? <Link className="text-link" to={commitUrl(projectId, repoId, sha)}>
              <code>{shortSha(sha)}</code> {commit.title}
            </Link> : commit.title || 'Commit SHA unavailable'}
          </li>
        })}</ul> : <p className="rule-message">No pull request commits on this page.</p>}
        <Pagination limit={page.limit} offset={page.offset} itemCount={page.items.length}
          hasMore={page.items.length === page.limit} onOffsetChange={setOffset} />
      </>}
    </ApiContent>
  </SectionRule>
}

function Discussion({ projectId, kind, repoId, number, onClose }: DiscussionTarget & {
  projectId: string; kind: DiscussionKind; onClose: () => void
}) {
  const [offset, setOffset] = useState(0)
  const { detail, comments } = useDiscussion(projectId, repoId, kind, number, { limit: detailLimit, offset })
  const item = detail.data
  const page = comments.data

  return <SectionRule title={item?.title || (kind === 'issue' ? 'Issue' : 'Pull request')} actions={
    <Button variant="ghost" size="sm" onClick={onClose}>Close details</Button>
  }>
    <ApiContent query={detail} loading="Loading discussion…">
      {item && <>
        <p className="metadata"><span>#{number}</span><span>{item.user?.login}</span><span>{formatDate(item.created_at)}</span></p>
        <SafeMarkdown content={item.body ?? ''} />
      </>}
    </ApiContent>
    <SectionRule title="Comments">
      <ApiContent query={comments} loading="Loading comments…">
        {page && <>
          {page.items.length ? <ul className="result-list">{page.items.map((comment, index) => <li key={comment.kind + '/' + (comment.id ?? index)}>
            <p className="metadata">
              <strong>{comment.user?.login}</strong><span>{formatDate(comment.created_at)}</span>
              {comment.path && <code>{comment.path}{comment.line != null ? ':' + comment.line : ''}</code>}
              {!comment.path && comment.line != null && <span>Line {comment.line}</span>}
            </p>
            <SafeMarkdown content={comment.body ?? ''} />
          </li>)}</ul> : <p className="rule-message">No comments on this page.</p>}
          <Pagination limit={page.limit} offset={page.offset} itemCount={page.items.length}
            hasMore={page.items.length === page.limit} onOffsetChange={setOffset} />
        </>}
      </ApiContent>
    </SectionRule>
    {kind === 'pull' && <PullCommits projectId={projectId} repoId={repoId} number={number} />}
  </SectionRule>
}

function Discussions({ projectId, repoId, repositories, kind }: {
  projectId: string; repoId: string; repositories: Repository[]; kind: DiscussionKind
}) {
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState<DiscussionTarget>()
  const query = useActivity(projectId, repoId, kind, { limit: listLimit, offset })
  const page = query.data

  return <div className={selected ? 'split-pane activity-pane' : undefined}>
    <div>
      <ApiContent query={query} loading={'Loading ' + views[kind].toLowerCase() + '…'}>
        {page && <>
          <DataTable label={views[kind]} columns={['Title', 'Repository', 'State', 'Updated']}
            empty={offset ? 'No more entries. Return to the previous page.' : 'No imported GitHub activity for this selection.'}
            rows={page.items.map((item) => {
              const { repository_id: repoId, number } = item
              const active = selected?.repoId === repoId && selected.number === number
              return { key: repoId + '/' + (number ?? item.id), cells: [
                number ? <button type="button" className="text-link" aria-pressed={active}
                  onClick={() => setSelected(active ? undefined : { repoId, number })}>
                  #{number} {item.title}
                </button> : item.title,
                repositories.find((repo) => repo.id === repoId)?.name ?? repoId,
                <StatusBadge status={item.state === 'open' ? 'Open' : item.state === 'closed' ? 'Closed' : item.state ?? '—'} />,
                formatDate(item.updated_at),
              ] }
            })} />
          <Pagination limit={page.limit} offset={page.offset} itemCount={page.items.length} hasMore={page.items.length === page.limit}
            onOffsetChange={(next) => { setOffset(next); setSelected(undefined) }} />
        </>}
      </ApiContent>
    </div>
    {selected && <Discussion key={selected.repoId + '/' + selected.number} projectId={projectId}
      kind={kind} {...selected} onClose={() => setSelected(undefined)} />}
  </div>
}

export function ActivityPage() {
  const project = useProject()
  const [view, setView] = useState<ActivityView>('commits')
  const [repoId, setRepoId] = useState('')
  const repositoriesQuery = useRepositories(project.id, { limit: 100 }, true)
  const repositories = repositoriesQuery.data?.items ?? []
  const selectedRepo = repositories.find((repo) => repo.id === repoId)
    ?? (view === 'commits' ? repositories[0] : undefined)

  return <>
    <PageHeading title="Activity" description="Browse imported Git history and GitHub discussions." />
    <div className="list-toolbar">
      <div className="subnav" role="group" aria-label="Activity view">
        {(Object.entries(views) as [ActivityView, string][]).map(([value, label]) => <button key={value} type="button"
          aria-pressed={view === value} onClick={() => setView(value)}>{label}</button>)}
      </div>
      <div className="filters">
        <Select label="Repository" value={selectedRepo?.id ?? ''} onChange={(event) => setRepoId(event.target.value)}>
          {(view !== 'commits' || !repositories.length) && <option value="">{view === 'commits' ? 'Select a repository' : 'All repositories'}</option>}
          {repositories.map((repo) => <option value={repo.id} key={repo.id}>{repo.name}</option>)}
        </Select>
        {selectedRepo?.source_type === 'github' && <RepositorySyncAction key={selectedRepo.id} repo={selectedRepo}
          kind="github_activity" label="Sync GitHub activity" />}
      </div>
    </div>
    <ApiContent query={repositoriesQuery} loading="Loading repositories…">{null}</ApiContent>
    {view === 'commits'
      ? selectedRepo ? <CommitActivity key={selectedRepo.id} repo={selectedRepo} />
        : repositoriesQuery.isSuccess && <p className="rule-message">Associate a repository to browse commits.</p>
      : <Discussions key={view + '/' + (selectedRepo?.id ?? '')} projectId={project.id} repoId={selectedRepo?.id ?? ''}
          repositories={repositories} kind={view} />}
  </>
}
