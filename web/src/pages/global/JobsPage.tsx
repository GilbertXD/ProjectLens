import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { DataTable } from '@/components/shared/DataTable'
import { KeyValueList } from '@/components/shared/KeyValueList'
import { PageHeading, SectionRule } from '@/components/shared/SectionRule'
import { formatDate } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Pagination } from '@/components/ui/Pagination'
import { Select } from '@/components/ui/Select'
import { StatusBadge } from '@/components/ui/StatusBadge'
import { jobTypes } from '@/mocks/fixtures'
import { isActiveJob, useMockConsole } from '@/mocks/MockConsoleProvider'
import type { Job } from '@/types/domain'

const limit = 10

function duration(job: Job, time: number) {
  if (!job.started_at) return '—'
  const end = job.finished_at ? Date.parse(job.finished_at) : time
  const seconds = Math.max(0, Math.floor((end - Date.parse(job.started_at)) / 1000))
  if (seconds < 60) return seconds + 's'
  if (seconds < 3600) return Math.floor(seconds / 60) + 'm ' + seconds % 60 + 's'
  return Math.floor(seconds / 3600) + 'h ' + Math.floor(seconds % 3600 / 60) + 'm'
}

const jobName = (type: string) => jobTypes[type as keyof typeof jobTypes] ?? type

export function JobsPage() {
  const store = useMockConsole()
  const [params, setParams] = useSearchParams()
  const [status, setStatus] = useState('')
  const [type, setType] = useState('')
  const [pageOffset, setOffset] = useState(0)
  const [time, setTime] = useState(Date.now)
  const jobs = store.jobs.filter((job) => (!status || job.status === status) && (!type || job.type === type))
  const offset = Math.min(pageOffset, Math.max(0, Math.ceil(jobs.length / limit) - 1) * limit)
  const selected = store.jobs.find((job) => job.id === params.get('job'))
  const hasRunning = selected?.status === 'running' || jobs.some((job) => job.status === 'running')
  useEffect(() => {
    if (!hasRunning) return
    const timer = window.setInterval(() => setTime(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [hasRunning])

  return <main id="main-content" tabIndex={-1} className="home-container global-page">
    <PageHeading title="Jobs" description="Inspect repository sync, scan, and import jobs." />
    <div className="filters">
      <Select label="Job status" value={status} onChange={(event) => { setStatus(event.target.value); setOffset(0) }}>
        <option value="">All statuses</option>
        <option value="pending">Pending</option><option value="running">Running</option>
        <option value="succeeded">Succeeded</option><option value="failed">Failed</option><option value="cancelled">Cancelled</option>
      </Select>
      <Select label="Job type" value={type} onChange={(event) => { setType(event.target.value); setOffset(0) }}>
        <option value="">All types</option>
        {Object.entries(jobTypes).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
      </Select>
    </div>
    <DataTable label="Jobs" columns={['Job', 'Status', 'Attempts', 'Duration', 'Updated']}
      empty="No jobs match these filters."
      rows={jobs.slice(offset, offset + limit).map((job) => ({ key: job.id, cells: [
        <button className="text-link" aria-pressed={selected?.id === job.id}
          onClick={() => setParams(selected?.id === job.id ? {} : { job: job.id })}>
          {jobName(job.type)} <code>{job.id.slice(0, 8)}</code>
        </button>, <span aria-live="polite"><StatusBadge status={job.status} /></span>,
        job.attempt + ' / ' + job.max_attempts, duration(job, time), formatDate(job.updated_at),
      ] }))} />
    <Pagination total={jobs.length} limit={limit} offset={offset} onOffsetChange={setOffset} />
    {selected && <SectionRule title="Job details" actions={
      <div className="inline-actions">
        {isActiveJob(selected) && <Button size="sm" variant="danger" onClick={() => store.cancelJob(selected.id)}>Cancel job</Button>}
        <Button size="sm" variant="secondary" onClick={() => setParams({})}>Close details</Button>
      </div>
    }>
      <KeyValueList entries={[
        ['Job ID', <code>{selected.id}</code>], ['Status', <StatusBadge status={selected.status} />],
        ['Created', formatDate(selected.created_at)], ['Scheduled', formatDate(selected.run_at)],
        ['Started', formatDate(selected.started_at)], ['Finished', formatDate(selected.finished_at)],
        ['Payload', <pre>{JSON.stringify(selected.payload, null, 2)}</pre>],
        ['Result', selected.result == null ? '—' : <pre>{JSON.stringify(selected.result, null, 2)}</pre>],
        ['Error message', selected.last_error ?? '—'], ['Error code', <code>{selected.last_error_code ?? '—'}</code>],
      ]} />
    </SectionRule>}
  </main>
}
