import { useState } from 'react'
import { Link } from 'react-router-dom'
import { DataTable } from '@/components/shared/DataTable'
import { KeyValueList } from '@/components/shared/KeyValueList'
import { ProjectDialog } from '@/components/shared/ProjectDialog'
import { PageHeading, SectionRule } from '@/components/shared/SectionRule'
import { formatDate, policyNames, repositoryPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { StatusBadge } from '@/components/ui/StatusBadge'
import { jobTypes } from '@/mocks/fixtures'
import { useMockConsole } from '@/mocks/MockConsoleProvider'
import { DocumentTable } from './DocumentTable'
import { useProject } from './workspace'

export function OverviewPage() {
  const project = useProject()
  const store = useMockConsole()
  const [editing, setEditing] = useState(false)
  const previews = store.repositories.filter(({ repository }) => repository.project_id === project.id)
  const repositories = previews.map(({ repository }) => repository)
  const documents = store.documents.filter((doc) => doc.project_id === project.id)
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at))
  const policies = previews.flatMap(({ repository, policies }) => policies.map((policy) => ({ repository, policy })))
  const jobs = store.jobs.filter((job) => job.payload.project_id === project.id)
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at)).slice(0, 5)

  return <>
    <PageHeading title="Overview" description={project.description} actions={<div className="inline-actions">
      <Button variant="secondary" size="sm" onClick={() => setEditing(true)}>Edit project</Button>
      <Button variant="ghost" size="sm" onClick={() => store.toggleProjectArchive(project)}>
        {project.status === 'active' ? 'Archive project' : 'Restore project'}
      </Button>
    </div>} />
    <KeyValueList entries={[
      ['Project', project.name], ['Slug', <code>{project.slug}</code>],
      ['Status', <StatusBadge status={project.status} />],
      ['Updated', formatDate(project.updated_at)],
    ]} />
    <SectionRule title="Associated repositories" actions={<Link className="text-link" to="../repositories">View all ({repositories.length})</Link>}>
      <DataTable label="Associated repositories" columns={['Name', 'Source', 'Role', 'Last sync']}
        empty="Associate a repository to inspect its files."
        rows={repositories.slice(0, 5).map((repo) => ({ key: repo.id, cells: [
          <Link className="text-link" to={repositoryPath(project.id, repo.id)}>{repo.name}</Link>,
          <StatusBadge status={repo.source_type} />, <StatusBadge status={repo.role} />, formatDate(repo.last_synced_at),
        ] }))} />
    </SectionRule>
    <SectionRule title="Recent documents" actions={<Link className="text-link" to="../documents">View all ({documents.length})</Link>}>
      <DocumentTable documents={documents.slice(0, 5)} repositories={repositories} />
    </SectionRule>
    <SectionRule title="Sync policies" actions={<span className="muted">{policies.filter(({ policy }) => policy.enabled).length} enabled</span>}>
      <DataTable label="Sync policies" columns={['Repository', 'Schedule', 'State', 'Interval']}
        empty="Open repository settings to set a sync schedule."
        rows={policies.map(({ repository, policy }) => ({ key: repository.id + policy.kind, cells: [
          <Link className="text-link" to={repositoryPath(project.id, repository.id) + '/settings'}>{repository.name}</Link>,
          policyNames[policy.kind], policy.enabled ? 'Enabled' : 'Disabled', policy.interval_seconds + 's',
        ] }))} />
    </SectionRule>
    <SectionRule title="Recent project jobs" actions={<Link className="text-link" to="/jobs">Global jobs</Link>}>
      <DataTable label="Recent project jobs" columns={['Job', 'Status', 'Updated']} empty="Sync a repository to start a job."
        rows={jobs.map((job) => ({ key: job.id, cells: [
          <Link className="text-link" to={'/jobs?job=' + encodeURIComponent(job.id)}>
            {jobTypes[job.type as keyof typeof jobTypes] ?? job.type}
          </Link>, <StatusBadge status={job.status} />, formatDate(job.updated_at),
        ] }))} />
    </SectionRule>
    {editing && <ProjectDialog project={project} onDismiss={() => setEditing(false)} onSaved={() => setEditing(false)} />}
  </>
}
