import { useId, useState } from 'react'
import type { FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { ApiContent, ApiFailure } from '@/api/ApiFeedback'
import { documentMaxBytes } from '@/api/endpoints'
import { useCreateDocument, useDocuments, useRepositories } from '@/api/queries'
import { PageHeading } from '@/components/shared/SectionRule'
import { documentPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
import { Pagination } from '@/components/ui/Pagination'
import { Select } from '@/components/ui/Select'
import { Textarea } from '@/components/ui/Textarea'
import type { Repository } from '@/types/domain'
import { DocumentTable } from './DocumentTable'
import { useProject } from './workspace'

function CreateDocument({ projectId, repositories, defaultOwner, onDismiss }: {
  projectId: string; repositories: Repository[]; defaultOwner: string; onDismiss: () => void
}) {
  const create = useCreateDocument(projectId)
  const navigate = useNavigate()
  const [content, setContent] = useState('')
  const [titleError, setTitleError] = useState('')
  const sizeId = useId()
  const bytes = new TextEncoder().encode(content).length
  const tooLarge = bytes > documentMaxBytes

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (create.isPending || tooLarge) return
    const form = new FormData(event.currentTarget)
    const title = String(form.get('title')).trim()
    if (!title) return setTitleError('Enter a document title.')
    const repository_id = String(form.get('owner') ?? '') || undefined
    try {
      const document = await create.mutateAsync({ title, content, repository_id })
      void navigate(documentPath(projectId, document.id))
    } catch {
      // The mutation error is displayed in the dialog with the draft still in place.
    }
  }

  return <Dialog open title="Create document" onDismiss={() => { if (!create.isPending) onDismiss() }}>
    <form className="dialog-form" onSubmit={(event) => { void submit(event) }}>
      <Input label="Title" name="title" required autoFocus error={titleError} disabled={create.isPending}
        onChange={() => setTitleError('')} />
      <Select label="Document owner" name="owner" defaultValue={defaultOwner} disabled={create.isPending}>
        <option value="">Project level</option>
        {repositories.map((repo) => <option key={repo.id} value={repo.id}>{repo.name}</option>)}
      </Select>
      <div>
        <Textarea label="Markdown" name="content" value={content} disabled={create.isPending}
          aria-describedby={sizeId} error={tooLarge ? 'Markdown must be at most 1 MiB.' : undefined}
          onChange={(event) => setContent(event.target.value)} />
        <p className="muted" id={sizeId}>{bytes.toLocaleString('en')} / {documentMaxBytes.toLocaleString('en')} bytes</p>
      </div>
      {create.error && <ApiFailure error={create.error} />}
      <div className="dialog__actions">
        <Button variant="secondary" onClick={onDismiss} disabled={create.isPending}>Cancel</Button>
        <Button type="submit" disabled={create.isPending || tooLarge}>{create.isPending ? 'Creating…' : 'Create document'}</Button>
      </div>
    </form>
  </Dialog>
}

export function DocumentsPage() {
  const project = useProject()
  const [filter, setFilter] = useState('all')
  const [offset, setOffset] = useState(0)
  const [creating, setCreating] = useState(false)
  const repositoryQuery = useRepositories(project.id, { limit: 100 }, true)
  const repositories = repositoryQuery.data?.items ?? []
  const repositoryId = ['all', 'project', 'repository'].includes(filter) ? undefined : filter
  const scope = filter === 'all' ? undefined : filter === 'project' ? 'project' : 'repository'
  const documents = useDocuments(project.id, { scope, repository_id: repositoryId, limit: 20, offset })

  return <>
    <PageHeading title="Documents" description="Read and edit Markdown owned by the project or its repositories."
      actions={<Button onClick={() => setCreating(true)}>Create document</Button>} />
    <div className="filters">
      <Select label="Document scope" value={filter} onChange={(event) => {
        setFilter(event.target.value)
        setOffset(0)
      }}>
        <option value="all">All</option><option value="project">Project</option><option value="repository">Repository</option>
        {repositoryId && !repositories.some(({ id }) => id === repositoryId) && <option value={repositoryId}>{repositoryId}</option>}
        {repositories.map((repo) => <option key={repo.id} value={repo.id}>{repo.name}</option>)}
      </Select>
    </div>
    {repositoryQuery.error && <ApiFailure error={repositoryQuery.error} hint="Repository names and owner choices may be unavailable."
      retry={() => { void repositoryQuery.refetch() }} />}
    <ApiContent query={documents} loading="Loading documents…">
      {documents.data && <>
        <DocumentTable documents={documents.data.items} repositories={repositories} />
        <Pagination total={documents.data.total} limit={documents.data.limit} offset={documents.data.offset} onOffsetChange={setOffset} />
      </>}
    </ApiContent>
    {creating && <CreateDocument projectId={project.id} repositories={repositories} defaultOwner={repositoryId ?? ''}
      onDismiss={() => setCreating(false)} />}
  </>
}
