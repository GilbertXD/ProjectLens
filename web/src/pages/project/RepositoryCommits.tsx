import { useId, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { ApiContent, ApiFailure } from '@/api/ApiFeedback'
import { isActiveJob, useCommit, useCommits, useCompareCommits, useEnqueueSync } from '@/api/queries'
import { useJobPoller } from '@/api/useJobPoller'
import { RefSelector } from '@/components/code/RefSelector'
import { DataTable } from '@/components/shared/DataTable'
import { KeyValueList } from '@/components/shared/KeyValueList'
import { SectionRule } from '@/components/shared/SectionRule'
import { formatDate, repositoryPath, shortSha } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Pagination } from '@/components/ui/Pagination'
import { StatusBadge } from '@/components/ui/StatusBadge'
import type { CommitHistoryEntry, FileChange, Repository } from '@/types/domain'

const limit = 20
const windowLabels = { complete: 'Complete', partial: 'Partial', window_limit_reached: 'Window limit reached' }
const fullSha = /^(?:[a-f0-9]{40}|[a-f0-9]{64})$/i

function ChangedFiles({ changes }: { changes: FileChange[] }) {
  return <DataTable label="Changed files" columns={['Path', 'Change', 'Added', 'Deleted']} empty="No changed files."
    rows={changes.map((change) => ({ key: change.path, cells: [
      <div className="stacked-cell"><code>{change.path}</code>{change.old_path && <small>From <code>{change.old_path}</code></small>}</div>,
      <div className="inline-actions">
        <span>{change.type}</span>
        {change.binary && <StatusBadge status="Binary" />}
      </div>,
      change.binary ? '—' : <span className="change-add">+{change.insertions}</span>,
      change.binary ? '—' : <span className="change-delete">−{change.deletions}</span>,
    ] }))} />
}

function UnifiedDiff({ patch }: { patch: string }) {
  let inHunk = false
  const lines = patch.split('\n')
  return <pre className="diff-output" aria-label="Unified diff" tabIndex={0}><code>
    {lines.map((line, index) => {
      if (line.startsWith('diff --git ')) inHunk = false
      if (line.startsWith('@@')) {
        inHunk = true
        return <span key={index} className="diff-hunk">{line}{'\n'}</span>
      }
      const style = inHunk && (line.startsWith('+') ? 'change-add' : line.startsWith('-') ? 'change-delete' : undefined)
      return <span key={index} className={style || undefined}>{line}{index < lines.length - 1 && '\n'}</span>
    })}
  </code></pre>
}

function Comparison({ repo, base, head }: { repo: Repository; base: string; head: string }) {
  const [offset, setOffset] = useState(0)
  const query = useCompareCommits(repo.project_id, repo.id, { base, head, limit, offset })
  const comparison = query.data

  return <ApiContent query={query} loading="Loading comparison…">
    {comparison && <>
      {comparison.truncated && <p className="rule-message">The changed file list was truncated.</p>}
      <ChangedFiles changes={comparison.items ?? []} />
      <Pagination total={comparison.total} limit={limit} offset={offset} onOffsetChange={setOffset} />
      {comparison.patch_truncated && <p className="rule-message" role="status">The diff patch was truncated.</p>}
      {comparison.patch ? <UnifiedDiff patch={comparison.patch} />
        : <p className="rule-message">No patch text is available for this comparison.</p>}
    </>}
  </ApiContent>
}

function ComparePanel({ repo, items }: { repo: Repository; items: CommitHistoryEntry[] }) {
  const optionsId = useId()
  const [baseInput, setBase] = useState<string>()
  const [headInput, setHead] = useState<string>()
  const base = baseInput ?? items[1]?.hash ?? ''
  const head = headInput ?? items[0]?.hash ?? ''
  const [compared, setCompared] = useState<{ base: string; head: string }>()
  const [error, setError] = useState('')

  return <SectionRule title="Compare commits">
    <form className="list-toolbar" onSubmit={(event) => {
      event.preventDefault()
      if (!fullSha.test(base.trim()) || !fullSha.test(head.trim())) {
        setError('Choose imported commits or enter full commit SHAs.')
        return
      }
      setError('')
      setCompared({ base: base.trim().toLowerCase(), head: head.trim().toLowerCase() })
    }}>
      <div className="filters">
        <Input label="Base commit" list={optionsId} value={base} error={error || undefined}
          onChange={(event) => setBase(event.target.value)} />
        <Input label="Head commit" list={optionsId} value={head} onChange={(event) => setHead(event.target.value)} />
        <datalist id={optionsId}>
          {items.map((item) => <option key={item.hash} value={item.hash}>{item.subject}</option>)}
        </datalist>
      </div>
      <Button type="submit">Show diff</Button>
    </form>
    {compared && <Comparison key={compared.base + '/' + compared.head} repo={repo} {...compared} />}
  </SectionRule>
}

function CommitDetail({ repo, sha }: { repo: Repository; sha: string }) {
  const [offset, setOffset] = useState(0)
  const { detail, files } = useCommit(repo.project_id, repo.id, sha, { limit, offset })
  const base = repositoryPath(repo.project_id, repo.id)
  const selected = detail.data

  return <SectionRule title="Commit details" actions={<div className="inline-actions">
    {selected && <Link className="text-link" to={base + '/files?commit=' + encodeURIComponent(selected.hash)}>Browse files at commit</Link>}
    <Link className="text-link" to={base + '/commits'}>Close details</Link>
  </div>}>
    <ApiContent query={detail} loading="Loading commit details…">
      {selected && <>
        <KeyValueList entries={[
          ['Subject', selected.subject], ['SHA', <code className="historical-sha">{selected.hash}</code>],
          ['Author', selected.author_name], ['Committed', formatDate(selected.committed_at)],
        ]} />
        {selected.message && selected.message.trim() !== selected.subject && <pre className="commit-message">{selected.message}</pre>}
      </>}
    </ApiContent>
    <ApiContent query={files} loading="Loading changed files…">
      {files.data && <>
        <ChangedFiles changes={files.data.items ?? []} />
        {files.data.truncated && <p className="rule-message">The changed file list was truncated.</p>}
        <Pagination total={files.data.total} limit={limit} offset={offset} onOffsetChange={setOffset} />
      </>}
    </ApiContent>
  </SectionRule>
}

export function RepositoryCommits({ repo, hash }: { repo: Repository; hash?: string }) {
  const [params] = useSearchParams()
  const sha = params.get('sha') || hash
  const [ref, setRef] = useState('')
  const [customRef, setCustomRef] = useState('')
  const [offset, setOffset] = useState(0)
  const [compare, setCompare] = useState(false)
  const query = useCommits(repo.project_id, repo.id, { ref, limit, offset })
  const history = query.data
  const items = history?.items ?? []
  const base = repositoryPath(repo.project_id, repo.id) + '/commits'
  const { mutation, receipt, refreshOnSettled } = useEnqueueSync(repo.project_id, repo.id, 'commits')
  const job = useJobPoller(receipt?.job_id, refreshOnSettled)
  const status = job.data?.status ?? receipt?.status
  const busy = mutation.isPending || Boolean(status && isActiveJob({ status }) && !job.error)

  function selectRef(next: string) {
    setRef(next)
    setOffset(0)
  }

  return <>
    <SectionRule title="Imported commits" actions={<div className="inline-actions">
      <Button size="sm" onClick={() => mutation.mutate(ref)} disabled={busy}>Sync commits</Button>
      <Button size="sm" variant="secondary" aria-pressed={compare} onClick={() => setCompare(!compare)}>Compare commits</Button>
    </div>}>
      <div className="list-toolbar">
        <RefSelector projectId={repo.project_id} repoId={repo.id} value={ref}
          onSelect={(next) => { setCustomRef(''); selectRef(next?.ref ?? '') }} />
        <form className="filters" onSubmit={(event) => { event.preventDefault(); selectRef(customRef.trim()) }}>
          <Input label="Custom ref" value={customRef} onChange={(event) => setCustomRef(event.target.value)} />
          <Button size="sm" variant="secondary" type="submit">Load ref</Button>
        </form>
      </div>
      {mutation.error && <ApiFailure error={mutation.error} />}
      {job.error && <ApiFailure error={job.error} retry={() => { void job.refetch() }} />}
      {receipt && status && <div className="metadata snapshot-job" aria-live="polite">
        <StatusBadge status={status} />
        <Link className="text-link" to={'/jobs?job=' + encodeURIComponent(receipt.job_id)}>View job</Link>
        {job.data?.last_error && <span>{job.data.last_error}</span>}
      </div>}
      <ApiContent query={query} loading="Loading imported commits…">
        {history ? <>
          <div className="metadata commit-window">
            <code>{history.ref}</code><StatusBadge status={windowLabels[history.window_status]} />
            <span>{history.total} imported</span><span>Window limit {history.window_limit}</span>
            <span>Last import {formatDate(history.synced_at)}</span>
          </div>
          {history.window_status === 'partial' && <p className="rule-message">Sync commits to import the next {history.page_size} commits.</p>}
          <DataTable label="Imported commits" columns={['Commit', 'SHA', 'Author', 'Committed']}
            rows={items.map((item) => ({ key: item.hash, cells: [
              <Link to={base + '?sha=' + encodeURIComponent(item.hash)} className="text-link" aria-current={sha === item.hash ? 'page' : undefined}>
                {item.subject}
              </Link>, <code>{shortSha(item.hash)}</code>, item.author_name, formatDate(item.committed_at),
            ] }))} />
          <Pagination total={history.total} limit={limit} offset={offset} onOffsetChange={setOffset} />
        </> : <p className="rule-message">No commit history has been imported for this ref.</p>}
      </ApiContent>
    </SectionRule>
    {sha && <CommitDetail key={sha} repo={repo} sha={sha} />}
    {compare && <ComparePanel repo={repo} items={items} />}
  </>
}
