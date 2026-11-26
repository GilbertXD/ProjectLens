import { useState } from 'react'
import type { FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { ApiContent, ApiFailure } from '@/api/ApiFeedback'
import { usePolicies, useRepositorySettings, useSetPolicy } from '@/api/queries'
import { RepositorySyncAction } from '@/api/RepositorySyncAction'
import { KeyValueList } from '@/components/shared/KeyValueList'
import { SectionRule } from '@/components/shared/SectionRule'
import { formatDate, policyNames, projectPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { Input } from '@/components/ui/Input'
import { Select } from '@/components/ui/Select'
import { Textarea } from '@/components/ui/Textarea'
import { useMockConsole } from '@/mocks/MockConsoleProvider'
import type { Repository, SyncPolicy } from '@/types/domain'

function PolicyForm({ repo, kind, policy }: { repo: Repository; kind: SyncPolicy['kind']; policy?: SyncPolicy }) {
  const label = policyNames[kind]
  const actionLabel = kind === 'github_activity' ? label : label.toLowerCase()
  const update = useSetPolicy(repo.project_id, repo.id, kind)

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    update.mutate({ enabled: form.has('enabled'), interval_seconds: Number(form.get('interval')) })
  }

  return <form className="policy-row" onSubmit={save} onChange={() => { if (!update.isPending) update.reset() }}>
    <strong>{label}</strong>
    <label className="check"><input name="enabled" type="checkbox" defaultChecked={policy?.enabled ?? false}
      disabled={update.isPending} />Enable {actionLabel} sync</label>
    <Input name="interval" type="number" label={label + ' interval (seconds)'} min={1} max={31536000}
      step={1} defaultValue={policy?.interval_seconds ?? 3600} disabled={update.isPending} required />
    <Button size="sm" variant="secondary" type="submit" disabled={update.isPending}>Save {actionLabel} policy</Button>
    {update.isSuccess && <span role="status">Saved</span>}
    {update.error && <ApiFailure error={update.error} />}
  </form>
}

export function RepositorySettings({ repo }: { repo: Repository }) {
  const navigate = useNavigate()
  const { disassociateRepository } = useMockConsole()
  const policies = usePolicies(repo.project_id, repo.id)
  const { save, refresh, remove } = useRepositorySettings(repo.project_id, repo.id)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [deleting, setDeleting] = useState(false)
  const kinds: SyncPolicy['kind'][] = repo.source_type === 'github' ? ['files', 'commits', 'github_activity'] : ['files', 'commits']
  const saving = save.isPending || refresh.isPending
  const apiError = save.error ?? refresh.error

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const name = String(form.get('name')).trim()
    if (!name) return setError('Enter a repository name.')
    setError('')
    setNotice('')
    refresh.reset()
    save.mutate({ name, description: String(form.get('description')).trim(),
      role: form.get('role') === 'primary' ? 'primary' : 'reference',
    }, { onSuccess: () => setNotice('Repository settings saved.') })
  }

  return <>
    <SectionRule title="Association">
      <form className="settings-form" onSubmit={submit}>
        <Input label="Name" name="name" defaultValue={repo.name} required error={error} />
        <Textarea label="Description" name="description" defaultValue={repo.description} />
        <Select label="Role" name="role" defaultValue={repo.role}>
          <option value="primary">Primary</option><option value="reference">Reference</option>
        </Select>
        <div className="inline-actions">
          <Button type="submit" disabled={saving}>Save repository</Button>
          <Button variant="secondary" disabled={saving} onClick={() => {
            setNotice('')
            save.reset()
            refresh.mutate(undefined, { onSuccess: () => setNotice('Metadata refreshed.') })
          }}>Refresh metadata</Button>
          {notice && <span role="status">{notice}</span>}
        </div>
        {apiError && <ApiFailure error={apiError} />}
      </form>
      <KeyValueList entries={[
        ['Source', <code>{repo.source}</code>], ['Default branch', <code>{repo.default_branch}</code>],
        ['Metadata updated', formatDate(repo.updated_at)],
      ]} />
    </SectionRule>
    <SectionRule title="Sync policies">
      <ApiContent query={policies} loading="Loading sync policies…">
        {kinds.map((kind) => <PolicyForm key={kind} repo={repo} kind={kind}
          policy={policies.data?.items.find((policy) => policy.kind === kind)} />)}
      </ApiContent>
    </SectionRule>
    <SectionRule title="Manual sync">
      <div className="inline-actions">
        <RepositorySyncAction repo={repo} kind="commits" label="Sync commits" />
        {repo.source_type === 'github' && <RepositorySyncAction repo={repo} kind="github_activity" label="Sync GitHub activity" />}
      </div>
    </SectionRule>
    <SectionRule title="Disassociate repository">
      <Button variant="danger" disabled={saving} onClick={() => setDeleting(true)}>Disassociate repository</Button>
    </SectionRule>
    <ConfirmDialog open={deleting} title="Disassociate repository" description={repo.name}
      impact={<>
        <p>This removes the repository’s snapshots, indexed files, imported history, GitHub activity, and sync policies. Active jobs receive cancellation requests. Repository-owned documents survive as project-level documents with read-only origin provenance.</p>
        {remove.error && <ApiFailure error={remove.error} />}
      </>}
      confirmLabel="Disassociate repository" pending={remove.isPending}
      onCancel={() => { if (!remove.isPending) { setDeleting(false); remove.reset() } }} onConfirm={() => {
        remove.mutate(undefined, { onSuccess: () => {
          // Keep the provisional Documents view consistent until it is migrated.
          disassociateRepository(repo)
          void navigate(projectPath(repo.project_id) + '/repositories')
        } })
      }} />
  </>
}
