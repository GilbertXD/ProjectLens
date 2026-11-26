import { useState } from 'react'
import { Link } from 'react-router-dom'
import { DataTable } from '@/components/shared/DataTable'
import { KeyValueList } from '@/components/shared/KeyValueList'
import { ProjectDialog } from '@/components/shared/ProjectDialog'
import { PageHeading, SectionRule } from '@/components/shared/SectionRule'
import { formatDate, policyNames, repositoryPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { StatusBadge } from '@/components/ui/StatusBadge'
import { ApiContent, ApiFailure } from '@/api/ApiFeedback'
import { jobLabel, useArchiveProject, useDocuments, useRecentProjectJobs, useRepositories, useRepositoryPolicies } from '@/api/queries'
import { DocumentTable } from './DocumentTable'
import { useProject } from './workspace'

export function OverviewPage() {
  const project = useProject()
  const [editing, setEditing] = useState(false)
  const archive = useArchiveProject()
  const repositoryQuery = useRepositories(project.id, { limit: 5, offset: 0 })
  const documentQuery = useDocuments(project.id, { limit: 5, offset: 0 })
  const repositories = repositoryQuery.data?.items ?? []
  const policies = useRepositoryPolicies(project.id, repositories)
  const policyRows = policies.flatMap((query, index) =>
    (query.data?.items ?? []).map((policy) => ({ repository: repositories[index], policy })))
  const jobs = useRecentProjectJobs(project.id)
  const policiesLoading = repositoryQuery.isPending || policies.some((query) => query.isPending)

  return <>
    <PageHeading title="Overview" description={project.description} actions={<div className="inline-actions">
      <Button variant="secondary" size="sm" onClick={() => setEditing(true)}>Edit project</Button>
      <Button variant="ghost" size="sm" disabled={archive.isPending} onClick={() => archive.mutate(project)}>
        {project.status === 'active' ? 'Archive project' : 'Restore project'}
      </Button>
    </div>} />
    {archive.error && <ApiFailure error={archive.error} />}
    <KeyValueList entries={[
      ['Project', project.name], ['Slug', <code>{project.slug}</code>],
      ['Status', <StatusBadge status={project.status} />],
      ['Updated', formatDate(project.updated_at)],
    ]} />
    <SectionRule title="Associated repositories" actions={<Link className="text-link" to="../repositories">View all{repositoryQuery.data && ` (${repositoryQuery.data.total})`}</Link>}>
      <ApiContent query={repositoryQuery} loading="Loading repositories…">
        <DataTable label="Associated repositories" columns={['Name', 'Source', 'Role', 'Last sync']}
          empty="Associate a repository to inspect its files."
          rows={repositories.map((repo) => ({ key: repo.id, cells: [
            <Link className="text-link" to={repositoryPath(project.id, repo.id)}>{repo.name}</Link>,
            <StatusBadge status={repo.source_type} />, <StatusBadge status={repo.role} />, formatDate(repo.last_synced_at),
          ] }))} />
      </ApiContent>
    </SectionRule>
    <SectionRule title="Recent documents" actions={<Link className="text-link" to="../documents">View all{documentQuery.data && ` (${documentQuery.data.total})`}</Link>}>
      <ApiContent query={documentQuery} loading="Loading recent documents…">
        <DocumentTable documents={documentQuery.data?.items ?? []} repositories={repositories} />
      </ApiContent>
    </SectionRule>
    <SectionRule title="Sync policies" actions={!policiesLoading && <span className="muted">{policyRows.filter(({ policy }) => policy.enabled).length} enabled</span>}>
      {policiesLoading && <p className="rule-message" role="status">Loading sync policies…</p>}
      {repositoryQuery.error && <ApiFailure error={repositoryQuery.error} retry={() => { void repositoryQuery.refetch() }} />}
      {policies.map((query, index) => query.error && <ApiFailure key={repositories[index].id} error={query.error}
        hint={'Could not load policies for ' + repositories[index].name + '.'} retry={() => { void query.refetch() }} />)}
      {!policiesLoading && !repositoryQuery.error && <DataTable label="Sync policies" columns={['Repository', 'Schedule', 'State', 'Interval']}
        empty="Open repository settings to set a sync schedule."
        rows={policyRows.map(({ repository, policy }) => ({ key: repository.id + policy.kind, cells: [
          <Link className="text-link" to={repositoryPath(project.id, repository.id) + '/settings'}>{repository.name}</Link>,
          policyNames[policy.kind], policy.enabled ? 'Enabled' : 'Disabled', policy.interval_seconds + 's',
        ] }))} />}
    </SectionRule>
    <SectionRule title="Recent project jobs" actions={<Link className="text-link" to="/jobs">Global jobs</Link>}>
      <ApiContent query={jobs} loading="Loading recent project jobs…">
        <DataTable label="Recent project jobs" columns={['Job', 'Status', 'Updated']} empty="Sync a repository to start a job."
          rows={(jobs.data ?? []).map((job) => ({ key: job.id, cells: [
            <Link className="text-link" to={'/jobs?job=' + encodeURIComponent(job.id)}>
              {jobLabel(job.type)}
            </Link>, <StatusBadge status={job.status} />, formatDate(job.updated_at),
          ] }))} />
      </ApiContent>
    </SectionRule>
    {editing && <ProjectDialog project={project} onDismiss={() => setEditing(false)} onSaved={() => setEditing(false)} />}
  </>
}
