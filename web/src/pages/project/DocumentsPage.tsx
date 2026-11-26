import { useState } from 'react'
import type { FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { PageHeading } from '@/components/shared/SectionRule'
import { documentPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
import { Select } from '@/components/ui/Select'
import { Textarea } from '@/components/ui/Textarea'
import { useMockConsole } from '@/mocks/MockConsoleProvider'
import type { Repository } from '@/types/domain'
import { DocumentTable } from './DocumentTable'
import { useProject } from './workspace'

function CreateDocument({ repositories, defaultOwner, onDismiss }: {
  repositories: Repository[]; defaultOwner: string; onDismiss: () => void
}) {
  const project = useProject()
  const store = useMockConsole()
  const navigate = useNavigate()
  const [error, setError] = useState('')

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const title = String(form.get('title')).trim()
    if (!title) return setError('Enter a document title.')
    const id = store.createDocument(project.id, { title, content: String(form.get('content')) },
      String(form.get('owner') ?? '') || undefined)
    void navigate(documentPath(project.id, id))
  }

  return <Dialog open title="Create document" onDismiss={onDismiss}>
    <form className="dialog-form" onSubmit={submit}>
      <Input label="Title" name="title" required autoFocus error={error} />
      <Select label="Document owner" name="owner" defaultValue={defaultOwner}>
        <option value="">Project level</option>
        {repositories.map((repo) => <option key={repo.id} value={repo.id}>{repo.name}</option>)}
      </Select>
      <Textarea label="Markdown" name="content" />
      <div className="dialog__actions">
        <Button variant="secondary" onClick={onDismiss}>Cancel</Button><Button type="submit">Create document</Button>
      </div>
    </form>
  </Dialog>
}

export function DocumentsPage() {
  const project = useProject()
  const store = useMockConsole()
  const [scope, setScope] = useState('all')
  const [creating, setCreating] = useState(false)
  const repositories = store.repositories.map(({ repository }) => repository).filter((repo) => repo.project_id === project.id)
  const documents = store.documents.filter((doc) => doc.project_id === project.id
    && (scope === 'all' || scope === doc.scope || scope === doc.repository_id))
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at))

  return <>
    <PageHeading title="Documents" description="Read and edit Markdown owned by the project or its repositories."
      actions={<Button onClick={() => setCreating(true)}>Create document</Button>} />
    <div className="filters">
      <Select label="Document scope" value={scope} onChange={(event) => setScope(event.target.value)}>
        <option value="all">All</option><option value="project">Project</option><option value="repository">Repository</option>
        {repositories.map((repo) => <option key={repo.id} value={repo.id}>{repo.name}</option>)}
      </Select>
    </div>
    <DocumentTable documents={documents} repositories={repositories} />
    {creating && <CreateDocument repositories={repositories} defaultOwner={repositories.some(({ id }) => id === scope) ? scope : ''}
      onDismiss={() => setCreating(false)} />}
  </>
}
