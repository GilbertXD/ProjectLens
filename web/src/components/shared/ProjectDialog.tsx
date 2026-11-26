import { useState } from 'react'
import type { FormEvent } from 'react'
import { useMockConsole } from '@/mocks/MockConsoleProvider'
import type { Project } from '@/types/domain'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Input } from '@/components/ui/Input'
import { Select } from '@/components/ui/Select'
import { Textarea } from '@/components/ui/Textarea'

export function ProjectDialog({ project, onDismiss, onSaved }: {
  project?: Project
  onDismiss: () => void
  onSaved: (id: string) => void
}) {
  const { saveProject } = useMockConsole()
  const [error, setError] = useState('')

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const slug = String(form.get('slug')).trim()
    const name = String(form.get('name')).trim()
    if (!name) return setError('Enter a project name.')
    const id = saveProject({
      slug, name, description: String(form.get('description')).trim(),
      status: form.get('status') === 'archived' ? 'archived' : 'active',
    }, project?.id)
    if (!id) return setError('This slug is already in use.')
    onSaved(id)
  }

  return <Dialog open title={project ? 'Edit project' : 'Create project'} onDismiss={onDismiss}>
    <form className="dialog-form" onSubmit={submit}>
      <Input label="Name" name="name" defaultValue={project?.name} required autoFocus />
      <Input label="Slug" name="slug" defaultValue={project?.slug} required pattern="[a-z0-9]+(-[a-z0-9]+)*"
        title="Use lowercase letters, numbers, and single hyphens." />
      <Textarea label="Description" name="description" defaultValue={project?.description} />
      <Select label="Status" name="status" defaultValue={project?.status ?? 'active'}>
        <option value="active">Active</option><option value="archived">Archived</option>
      </Select>
      {error && <p role="alert" className="field-error">{error}</p>}
      <div className="dialog__actions">
        <Button variant="secondary" onClick={onDismiss}>Cancel</Button>
        <Button type="submit">{project ? 'Save project' : 'Create project'}</Button>
      </div>
    </form>
  </Dialog>
}
