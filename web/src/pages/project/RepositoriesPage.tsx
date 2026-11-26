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
import { useMockConsole } from '@/mocks/MockConsoleProvider'
import type { RepositorySourceType } from '@/types/domain'
import { useProject } from './workspace'

function AssociateDialog({ projectId, onDismiss }: { projectId: string; onDismiss: () => void }) {
  const { associateRepository } = useMockConsole()
  const [sourceType, setSourceType] = useState<RepositorySourceType>('local_git')
  const [error, setError] = useState('')

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const owner = String(form.get('owner')).trim()
    const repo = String(form.get('repository')).trim()
    const localPath = String(form.get('source')).trim().replace(/\/+$/, '')
    if (sourceType === 'local_git' && !localPath.startsWith('/')) return setError('Enter an absolute local Git path.')
    if (sourceType === 'github' && (!/^[\w-]+$/.test(owner) || !/^[\w.-]+$/.test(repo))) {
      return setError('Enter a GitHub owner and repository name.')
    }
    const name = String(form.get('name')).trim() || (sourceType === 'github' ? owner + '/' + repo : localPath.split('/').at(-1)!)
    const source = sourceType === 'github' ? 'https://github.com/' + owner + '/' + repo : localPath
    if (!associateRepository(projectId, { name, source, source_type: sourceType,
      description: String(form.get('description')).trim(), role: form.get('role') === 'primary' ? 'primary' : 'reference' })) {
      return setError('This source is already associated with the project.')
    }
    onDismiss()
  }

  return <Dialog open title="Associate repository" onDismiss={onDismiss}>
    <form className="dialog-form" onSubmit={submit}>
      <Select label="Source type" value={sourceType} onChange={(event) => {
        setSourceType(event.target.value === 'github' ? 'github' : 'local_git')
        setError('')
      }}>
        <option value="local_git">Local Git</option><option value="github">GitHub</option>
      </Select>
      {sourceType === 'local_git' ? <Input label="Local Git path" name="source" required placeholder="/work/repository" />
        : <><Input label="GitHub owner" name="owner" required /><Input label="GitHub repository" name="repository" required /></>}
      <Select label="Role" name="role"><option value="primary">Primary</option><option value="reference">Reference</option></Select>
      <Input label="Display name" name="name" />
      <Textarea label="Description" name="description" />
      {error && <p role="alert" className="field-error">{error}</p>}
      <div className="dialog__actions">
        <Button variant="secondary" onClick={onDismiss}>Cancel</Button><Button type="submit">Associate repository</Button>
      </div>
    </form>
  </Dialog>
}

export function RepositoriesPage() {
  const project = useProject()
  const store = useMockConsole()
  const [role, setRole] = useState('')
  const [source, setSource] = useState('')
  const [associating, setAssociating] = useState(false)
  const [synced, setSynced] = useState('')
  const repositories = store.repositories.map(({ repository }) => repository)
    .filter((repo) => repo.project_id === project.id && (!role || repo.role === role) && (!source || repo.source_type === source))

  return <>
    <PageHeading title="Repositories" description="Inspect indexed source and manage repository associations."
      actions={<Button onClick={() => setAssociating(true)}>Associate repository</Button>} />
    <div className="filters">
      <Select label="Role" value={role} onChange={(event) => setRole(event.target.value)}>
        <option value="">All roles</option><option value="primary">Primary</option><option value="reference">Reference</option>
      </Select>
      <Select label="Source type" value={source} onChange={(event) => setSource(event.target.value)}>
        <option value="">All source types</option><option value="local_git">Local Git</option><option value="github">GitHub</option>
      </Select>
    </div>
    {synced && <p className="rule-message" role="status">Sync requested for {synced}. <Link className="text-link" to="/jobs">View jobs</Link></p>}
    <DataTable label="Repositories" columns={['Name', 'Source', 'Role', 'Last sync', 'Actions']}
      empty="No repositories match these filters. Change the filters or associate a repository."
      rows={repositories.map((repo) => ({ key: repo.id, cells: [
        <div className="stacked-cell">
          <Link className="text-link" to={repositoryPath(project.id, repo.id)}>{repo.name}</Link>
          <span className="muted">{repo.description}</span>
        </div>, <StatusBadge status={repo.source_type} />, <StatusBadge status={repo.role} />, formatDate(repo.last_synced_at),
        <Button size="sm" variant="secondary" onClick={() => { store.syncFiles(repo); setSynced(repo.name) }}>Sync files</Button>,
      ] }))} />
    {associating && <AssociateDialog projectId={project.id} onDismiss={() => setAssociating(false)} />}
  </>
}
