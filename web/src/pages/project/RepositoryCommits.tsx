import { useState } from 'react'
import { Link } from 'react-router-dom'
import { DataTable } from '@/components/shared/DataTable'
import { KeyValueList } from '@/components/shared/KeyValueList'
import { SectionRule } from '@/components/shared/SectionRule'
import { formatDate, repositoryPath, shortSha } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Select } from '@/components/ui/Select'
import { StatusBadge } from '@/components/ui/StatusBadge'
import type { RepositoryPreview } from '@/mocks/fixtures'
import type { FileChange } from '@/types/domain'

export function ChangedFiles({ changes }: { changes: FileChange[] }) {
  return <DataTable label="Changed files" columns={['Path', 'Change', 'Added', 'Deleted']} empty="No changed files in this preview."
    rows={changes.map((change) => ({ key: change.path, cells: [
      <code>{change.old_path && <>{change.old_path} / </>}{change.path}</code>, change.type,
      change.binary ? 'Binary' : <span className="change-add">+{change.insertions}</span>,
      change.binary ? '—' : <span className="change-delete">−{change.deletions}</span>,
    ] }))} />
}

function ComparePanel({ preview }: { preview: RepositoryPreview }) {
  const items = preview.history?.items ?? []
  const [base, setBase] = useState(items[1]?.hash ?? '')
  const [head, setHead] = useState(items[0]?.hash ?? '')
  const [compared, setCompared] = useState<{ base: string; head: string }>()
  const comparison = preview.comparisons.find((item) => item.base === compared?.base && item.head === compared?.head)

  return <SectionRule title="Compare commits">
    <form className="list-toolbar" onSubmit={(event) => { event.preventDefault(); setCompared({ base, head }) }}>
      <div className="filters">
        <Select label="Base commit" value={base} onChange={(event) => { setBase(event.target.value); setCompared(undefined) }} required>
          <option value="">Select a commit</option>
          {items.map((item) => <option key={item.hash} value={item.hash}>{shortSha(item.hash)} {item.subject}</option>)}
        </Select>
        <Select label="Head commit" value={head} onChange={(event) => { setHead(event.target.value); setCompared(undefined) }} required>
          <option value="">Select a commit</option>
          {items.map((item) => <option key={item.hash} value={item.hash}>{shortSha(item.hash)} {item.subject}</option>)}
        </Select>
      </div>
      <Button type="submit">Show diff</Button>
    </form>
    {compared && (comparison ? <>
      <ChangedFiles changes={comparison.items} />
      {(comparison.truncated || comparison.patch_truncated) && <p className="rule-message">The comparison was truncated.</p>}
      <pre className="diff-output" aria-label="Unified diff" tabIndex={0}>
        {comparison.patch.split('\n').map((line, index) => <span key={index}
          className={line.startsWith('+') && !line.startsWith('+++') ? 'change-add'
            : line.startsWith('-') && !line.startsWith('---') ? 'change-delete' : undefined}>{line}{'\n'}</span>)}
      </pre>
    </> : <p className="rule-message">{compared.base === compared.head ? 'The selected commits are identical.' : 'No diff is available for this pair in the preview.'}</p>)}
  </SectionRule>
}

const windowLabels = { complete: 'Complete', partial: 'Partial', window_limit_reached: 'Window limit reached' }

export function RepositoryCommits({ preview, hash }: { preview: RepositoryPreview; hash?: string }) {
  const { repository, history, changes } = preview
  const [compare, setCompare] = useState(false)
  const selected = history?.items.find((item) => item.hash === hash)
  const base = repositoryPath(repository.project_id, repository.id) + '/commits'

  if (!history) return <SectionRule title="Commits"><p className="rule-message">No commit history has been imported.</p></SectionRule>

  return <>
    <SectionRule title="Imported commits" actions={<Button size="sm" variant="secondary" aria-pressed={compare}
      disabled={history.items.length < 2} onClick={() => setCompare(!compare)}>Compare commits</Button>}>
      <div className="metadata commit-window">
        <code>{history.ref}</code><StatusBadge status={windowLabels[history.window_status]} />
        <span>{history.total} imported</span><span>Last import {formatDate(history.synced_at)}</span>
      </div>
      <DataTable label="Imported commits" columns={['Commit', 'SHA', 'Author', 'Committed']}
        rows={history.items.map((item) => ({ key: item.hash, cells: [
          <Link to={base + '/' + item.hash} className="text-link" aria-current={selected?.hash === item.hash ? 'page' : undefined}>
            {item.subject}
          </Link>, <code>{shortSha(item.hash)}</code>, item.author_name, formatDate(item.committed_at),
        ] }))} />
    </SectionRule>
    {hash && <SectionRule title="Commit details" actions={<Link to={base} className="text-link">Close details</Link>}>
      {selected ? <>
        <KeyValueList entries={[
          ['Subject', selected.subject], ['SHA', <code>{selected.hash}</code>],
          ['Author', selected.author_name], ['Committed', formatDate(selected.committed_at)],
        ]} />
        <ChangedFiles changes={changes[selected.hash] ?? []} />
      </> : <p className="rule-message">This commit is outside the imported history.</p>}
    </SectionRule>}
    {compare && <ComparePanel preview={preview} />}
  </>
}
