import { Link, Navigate, NavLink, useLocation, useParams } from 'react-router-dom'
import { DataTable } from '@/components/shared/DataTable'
import { PageHeading, SectionRule } from '@/components/shared/SectionRule'
import { formatDate, projectPath, repositoryPath, shortSha } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { StatusBadge } from '@/components/ui/StatusBadge'
import type { RepositoryPreview } from '@/mocks/fixtures'
import { isActiveJob, useMockConsole } from '@/mocks/MockConsoleProvider'
import { RepositoryCommits } from './RepositoryCommits'
import { RepositoryFiles } from './RepositoryFiles'
import { RepositorySettings } from './RepositorySettings'
import { useProject } from './workspace'

const tabs = { files: 'Files', commits: 'Commits', snapshots: 'Snapshots', settings: 'Settings' }

function Snapshots({ preview }: { preview: RepositoryPreview }) {
  const store = useMockConsole()
  const repo = preview.repository
  const job = store.jobs.find((item) => item.type === 'repository.sync' && item.payload.repository_id === repo.id)

  return <SectionRule title="Snapshots" actions={<Button onClick={() => store.syncFiles(repo)}>Publish snapshot</Button>}>
    {job && <div className="metadata snapshot-job" aria-live="polite">
      <Link className="text-link" to={'/jobs?job=' + encodeURIComponent(job.id)}>
        {isActiveJob(job) ? 'Publication job' : 'Latest file sync job'}
      </Link>
      <StatusBadge status={job.status} />
    </div>}
    <DataTable label="Snapshots" columns={['Version', 'Status', 'Commit', 'Included', 'Skipped', 'File deltas', 'Published']}
      empty="No snapshots yet. Publish a snapshot to index files."
      rows={preview.snapshots.map((item) => ({ key: item.id, cells: [
        'v' + item.version, <StatusBadge status={item.status} />,
        item.commit_hash ? <code title={item.commit_hash}>{shortSha(item.commit_hash)}</code> : '—',
        item.included_files, item.skipped_files,
        <span className="metadata deltas"><span className="change-add" aria-label="Added">+{item.added_files}</span>
          <span aria-label="Modified">~{item.modified_files}</span><span className="change-delete" aria-label="Deleted">−{item.deleted_files}</span></span>,
        formatDate(item.published_at),
      ] }))} />
  </SectionRule>
}

export function RepositoryDetailPage() {
  const project = useProject()
  const { repoId, '*': rest } = useParams()
  const { search, hash: fragment } = useLocation()
  const store = useMockConsole()
  const preview = store.repositories.find(({ repository }) => repository.project_id === project.id && repository.id === repoId)
  const back = projectPath(project.id) + '/repositories'
  if (!preview) return <><PageHeading title="Repository not found" /><Link className="text-link" to={back}>All repositories</Link></>

  const repo = preview.repository
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
    {tab === 'files' && !hash ? <RepositoryFiles key={repo.id} preview={preview} />
      : tab === 'commits' && !extra ? <RepositoryCommits key={repo.id} preview={preview} hash={hash} />
      : tab === 'snapshots' && !hash ? <Snapshots preview={preview} />
      : tab === 'settings' && !hash ? <RepositorySettings key={repo.id} preview={preview} />
      : <p className="rule-message">Choose a repository section above.</p>}
  </>
}
