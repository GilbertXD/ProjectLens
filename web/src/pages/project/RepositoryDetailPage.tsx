import { useState } from 'react'
import { Link, Navigate, NavLink, useLocation, useParams } from 'react-router-dom'
import { DataTable } from '@/components/shared/DataTable'
import { PageHeading, SectionRule } from '@/components/shared/SectionRule'
import { formatDate, projectPath, repositoryPath, shortSha } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { StatusBadge } from '@/components/ui/StatusBadge'
import { ApiContent } from '@/api/ApiFeedback'
import { useRepository, useSnapshots } from '@/api/queries'
import { useRepositorySync } from '@/api/RepositorySyncAction'
import { Pagination } from '@/components/ui/Pagination'
import type { Repository } from '@/types/domain'
import { RepositoryCommits } from './RepositoryCommits'
import { RepositoryFiles } from './RepositoryFiles'
import { RepositorySettings } from './RepositorySettings'
import { useProject } from './workspace'

const tabs = { files: 'Files', commits: 'Commits', snapshots: 'Snapshots', settings: 'Settings' }

function Snapshots({ repo }: { repo: Repository }) {
  const [offset, setOffset] = useState(0)
  const { snapshots, active } = useSnapshots(repo.project_id, repo.id, { limit: 10, offset })
  const sync = useRepositorySync(repo, 'files')
  const page = snapshots.data

  return <SectionRule title="Snapshots" actions={<Button disabled={sync.busy} onClick={sync.start}>Publish snapshot</Button>}>
    {sync.feedback}
    <ApiContent query={active} loading="Checking the active snapshot…">
      <p className="rule-message">
        {active.data ? <>Active snapshot <code>v{active.data.version}</code> on <code>{active.data.resolved_ref}</code></>
          : 'No active snapshot. Publish a snapshot to make files searchable.'}
      </p>
    </ApiContent>
    <ApiContent query={snapshots} loading="Loading snapshots…">
      <DataTable label="Snapshots" columns={['Version', 'Status', 'Commit', 'Included', 'Skipped', 'File deltas', 'Published']}
        empty="No snapshots yet. Publish a snapshot to index files."
        rows={(page?.items ?? []).map((item) => ({ key: item.id, cells: [
          'v' + item.version, <StatusBadge status={item.status} />,
          item.commit_hash ? <code title={item.commit_hash}>{shortSha(item.commit_hash)}</code> : '—',
          item.included_files, item.skipped_files,
          <span className="metadata deltas"><span className="change-add" aria-label="Added">+{item.added_files}</span>
            <span aria-label="Modified">~{item.modified_files}</span><span className="change-delete" aria-label="Deleted">−{item.deleted_files}</span></span>,
          formatDate(item.published_at),
        ] }))} />
      {page && <Pagination total={page.total} limit={page.limit} offset={page.offset} onOffsetChange={setOffset} />}
    </ApiContent>
  </SectionRule>
}

export function RepositoryDetailPage() {
  const project = useProject()
  const { repoId, '*': rest } = useParams()
  const { search, hash: fragment } = useLocation()
  const query = useRepository(project.id, repoId ?? '')
  const back = projectPath(project.id) + '/repositories'
  const repo = query.data
  if (!repo || query.error) return <>
    <ApiContent query={query} loading="Loading repository…">{null}</ApiContent>
    <Link className="text-link" to={back}>All repositories</Link>
  </>

  const base = repositoryPath(project.id, repo.id)
  const [tab, hash, extra] = (rest ?? '').split('/')
  if (!tab) return <Navigate to={{ pathname: base + '/files', search, hash: fragment }} replace />

  return <>
    <p className="back-link"><Link className="text-link" to={back}>All repositories</Link></p>
    <PageHeading title={repo.name} description={repo.description} actions={<div className="metadata">
      <StatusBadge status={repo.source_type} /><StatusBadge status={repo.role} />
    </div>} />
    <nav className="subnav" aria-label="Repository sections">
      {Object.entries(tabs).map(([path, label]) => <NavLink key={path} to={base + '/' + path}>{label}</NavLink>)}
    </nav>
    {tab === 'files' && !hash ? <RepositoryFiles key={repo.id} repo={repo} />
      : tab === 'commits' && !extra ? <RepositoryCommits key={repo.id} repo={repo} hash={hash} />
      : tab === 'snapshots' && !hash ? <Snapshots key={repo.id} repo={repo} />
      : tab === 'settings' && !hash ? <RepositorySettings key={repo.id} repo={repo} />
      : <p className="rule-message">Choose a repository section above.</p>}
  </>
}
