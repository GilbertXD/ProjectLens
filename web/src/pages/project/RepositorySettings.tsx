import { useState } from 'react'
import type { FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { KeyValueList } from '@/components/shared/KeyValueList'
import { SectionRule } from '@/components/shared/SectionRule'
import { formatDate, policyNames, projectPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { Input } from '@/components/ui/Input'
import { Select } from '@/components/ui/Select'
import { Textarea } from '@/components/ui/Textarea'
import { defaultSyncPolicy } from '@/mocks/fixtures'
import type { RepositoryPreview } from '@/mocks/fixtures'
import { useMockConsole } from '@/mocks/MockConsoleProvider'
import type { SyncPolicy } from '@/types/domain'

function PolicyForm({ policy, onSave }: { policy: SyncPolicy; onSave: (policy: SyncPolicy) => void }) {
  const label = policyNames[policy.kind]
  const actionLabel = policy.kind === 'github_activity' ? label : label.toLowerCase()
  const [saved, setSaved] = useState(false)

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const interval = Number(form.get('interval'))
    if (!Number.isSafeInteger(interval) || interval < 60) return
    onSave({ ...policy, enabled: form.has('enabled'), interval_seconds: interval })
    setSaved(true)
  }

  return <form className="policy-row" onSubmit={save} onChange={() => setSaved(false)}>
    <strong>{label}</strong>
    <label className="check"><input name="enabled" type="checkbox" defaultChecked={policy.enabled} />Enable {actionLabel} sync</label>
    <Input name="interval" type="number" label={label + ' interval (seconds)'} min={60} step={1} defaultValue={policy.interval_seconds} required />
    <Button size="sm" variant="secondary" type="submit">Save {actionLabel} policy</Button>
    {saved && <span role="status">Saved</span>}
  </form>
}

export function RepositorySettings({ preview }: { preview: RepositoryPreview }) {
  const repo = preview.repository
  const store = useMockConsole()
  const navigate = useNavigate()
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [deleting, setDeleting] = useState(false)
  const kinds: SyncPolicy['kind'][] = repo.source_type === 'github' ? ['files', 'commits', 'github_activity'] : ['files', 'commits']

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const form = new FormData(event.currentTarget)
    const name = String(form.get('name')).trim()
    if (!name) return setError('Enter a repository name.')
    store.editRepository(repo.id, { name, description: String(form.get('description')).trim(),
      role: form.get('role') === 'primary' ? 'primary' : 'reference' })
    setError('')
    setNotice('Repository settings saved.')
  }

  return <>
    <SectionRule title="Association">
      <form className="settings-form" onSubmit={save}>
        <Input label="Name" name="name" defaultValue={repo.name} required error={error} />
        <Textarea label="Description" name="description" defaultValue={repo.description} />
        <Select label="Role" name="role" defaultValue={repo.role}>
          <option value="primary">Primary</option><option value="reference">Reference</option>
        </Select>
        <div className="inline-actions">
          <Button type="submit">Save repository</Button>
          <Button variant="secondary" onClick={() => { store.refreshRepository(repo.id); setNotice('Metadata refreshed.') }}>
            Refresh metadata
          </Button>
          <span role="status">{notice}</span>
        </div>
      </form>
      <KeyValueList entries={[
        ['Source', <code>{repo.source}</code>], ['Default branch', <code>{repo.default_branch}</code>],
        ['Metadata updated', formatDate(repo.updated_at)],
      ]} />
    </SectionRule>
    <SectionRule title="Sync policies">
      {kinds.map((kind) => <PolicyForm key={kind}
        policy={preview.policies.find((policy) => policy.kind === kind) ?? defaultSyncPolicy(kind)}
        onSave={(policy) => store.updatePolicy(repo.id, policy)} />)}
    </SectionRule>
    <SectionRule title="Disassociate repository">
      <Button variant="danger" onClick={() => setDeleting(true)}>Disassociate repository</Button>
    </SectionRule>
    <ConfirmDialog open={deleting} title="Disassociate repository" description={repo.name}
      impact="This removes the repository’s snapshots, indexed files, imported history, GitHub activity, and sync policies. Active jobs are cancelled. Repository-owned documents survive as project-level documents with read-only origin provenance."
      confirmLabel="Disassociate repository" onCancel={() => setDeleting(false)} onConfirm={() => {
        store.disassociateRepository(repo)
        void navigate(projectPath(repo.project_id) + '/repositories')
      }} />
  </>
}
