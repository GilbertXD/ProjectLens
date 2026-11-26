import { useState } from 'react'
import type { FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { DataTable } from '@/components/shared/DataTable'
import { PageHeading } from '@/components/shared/SectionRule'
import { formatDate, repositoryPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
import { Select } from '@/components/ui/Select'
import { StatusBadge } from '@/components/ui/StatusBadge'
import { Textarea } from '@/components/ui/Textarea'
import { ApiContent, ApiFailure } from '@/api/ApiFeedback'
import { ApiError } from '@/api/client'
import { useAssociateRepository, useRepositories } from '@/api/queries'
import { RepositorySyncAction } from '@/api/RepositorySyncAction'
import { Pagination } from '@/components/ui/Pagination'
import type { RepositoryRole, RepositorySourceType } from '@/types/domain'
import { useProject } from './workspace'

const associationHints: Record<number, string> = {
  400: 'Check the source details and try again.',
  403: 'For a local repository, confirm the server path is under an allowed Git root.',
  409: 'Choose a source that is not already associated with this project.',
  429: 'Wait for the upstream rate limit to reset, then try again.',
  502: 'Check that the upstream repository is reachable and try again.',
}

function AssociateDialog({ projectId, onDismiss }: { projectId: string; onDismiss: () => void }) {
  const associate = useAssociateRepository(projectId)
  const [sourceType, setSourceType] = useState<RepositorySourceType>('local_git')
  const [error, setError] = useState('')

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const owner = String(form.get('owner')).trim()
    const repo = String(form.get('repository')).trim()
    const localPath = String(form.get('source') ?? '').trim()
    if (sourceType === 'local_git' && !localPath.startsWith('/')) return setError('Enter an absolute local Git path.')
    if (sourceType === 'github' && (!/^[\w-]+$/.test(owner) || !/^[\w.-]+$/.test(repo))) {
      return setError('Enter a GitHub owner and repository name.')
    }
    const name = String(form.get('name')).trim() || undefined
    const source = sourceType === 'github' ? 'https://github.com/' + owner + '/' + repo : localPath
    setError('')
    associate.mutate({ name, source, source_type: sourceType,
      description: String(form.get('description')).trim(), role: form.get('role') === 'primary' ? 'primary' : 'reference',
    }, { onSuccess: onDismiss })
  }

  return <Dialog open title="Associate repository" onDismiss={() => { if (!associate.isPending) onDismiss() }}>
    <form className="dialog-form" onSubmit={submit}>
      <Select label="Source type" disabled={associate.isPending} value={sourceType} onChange={(event) => {
        setSourceType(event.target.value === 'github' ? 'github' : 'local_git')
        setError('')
        associate.reset()
      }}>
        <option value="local_git">Local Git</option><option value="github">GitHub</option>
      </Select>
      {sourceType === 'local_git' ? <Input label="Local Git path" name="source" required placeholder="/work/repository" />
        : <><Input label="GitHub owner" name="owner" required /><Input label="GitHub repository" name="repository" required /></>}
      <Select label="Role" name="role"><option value="primary">Primary</option><option value="reference">Reference</option></Select>
      <Input label="Display name" name="name" />
      <Textarea label="Description" name="description" />
      {error && <p role="alert" className="field-error">{error}</p>}
      {associate.error && <ApiFailure error={associate.error}
        hint={associate.error instanceof ApiError ? associationHints[associate.error.status] : undefined} />}
      <div className="dialog__actions">
        <Button variant="secondary" disabled={associate.isPending} onClick={onDismiss}>Cancel</Button><Button type="submit" disabled={associate.isPending}>Associate repository</Button>
      </div>
    </form>
  </Dialog>
}

export function RepositoriesPage() {
  const project = useProject()
  const [role, setRole] = useState<RepositoryRole | ''>('')
  const [source, setSource] = useState<RepositorySourceType | ''>('')
  const [offset, setOffset] = useState(0)
  const [associating, setAssociating] = useState(false)
  const query = useRepositories(project.id, { role: role || undefined, source_type: source || undefined, limit: 10, offset })
  const page = query.data

  return <>
    <PageHeading title="Repositories" description="Inspect indexed source and manage repository associations."
      actions={<Button onClick={() => setAssociating(true)}>Associate repository</Button>} />
    <div className="filters">
      <Select label="Role" value={role} onChange={(event) => { setRole(event.target.value as RepositoryRole | ''); setOffset(0) }}>
        <option value="">All roles</option><option value="primary">Primary</option><option value="reference">Reference</option>
      </Select>
      <Select label="Source type" value={source} onChange={(event) => { setSource(event.target.value as RepositorySourceType | ''); setOffset(0) }}>
        <option value="">All source types</option><option value="local_git">Local Git</option><option value="github">GitHub</option>
      </Select>
    </div>
    <ApiContent query={query} loading="Loading repositories…">
      <DataTable label="Repositories" columns={['Name', 'Source', 'Role', 'Last sync', 'Actions']}
        empty="No repositories match these filters. Change the filters or associate a repository."
        rows={(page?.items ?? []).map((repo) => ({ key: repo.id, cells: [
          <div className="stacked-cell">
            <Link className="text-link" to={repositoryPath(project.id, repo.id)}>{repo.name}</Link>
            <span className="muted">{repo.description}</span>
          </div>, <StatusBadge status={repo.source_type} />, <StatusBadge status={repo.role} />, formatDate(repo.last_synced_at),
          <RepositorySyncAction repo={repo} kind="files" label="Sync files" />,
        ] }))} />
      {page && <Pagination total={page.total} limit={page.limit} offset={page.offset} onOffsetChange={setOffset} />}
    </ApiContent>
    {associating && <AssociateDialog projectId={project.id} onDismiss={() => setAssociating(false)} />}
  </>
}
