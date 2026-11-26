import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/Button'
import { StatusBadge } from '@/components/ui/StatusBadge'
import type { Repository, SyncPolicy } from '@/types/domain'
import { ApiFailure } from './ApiFeedback'
import { ApiError } from './client'
import { isActiveJob, useEnqueueSync } from './queries'
import { useJobPoller } from './useJobPoller'

export function useRepositorySync(repo: Repository, kind: SyncPolicy['kind']) {
  const { mutation, receipt, refreshOnSettled } = useEnqueueSync(repo.project_id, repo.id, kind)
  const job = useJobPoller(receipt?.job_id, refreshOnSettled)
  const status = job.data?.status ?? receipt?.status
  return {
    start: () => mutation.mutate(),
    busy: mutation.isPending || Boolean(status && isActiveJob({ status }) && !job.error),
    feedback: <>
      {mutation.error && <ApiFailure error={mutation.error}
        hint={mutation.error instanceof ApiError && mutation.error.status === 409
          ? 'Wait for the active sync to finish before trying again.' : undefined} />}
      {job.error && <ApiFailure error={job.error} retry={() => { void job.refetch() }} />}
      {receipt && status && <div className="metadata snapshot-job" aria-live="polite">
        <span>{receipt.reused ? 'Reused active job' : 'Job queued'}</span>
        <StatusBadge status={status} />
        <Link className="text-link" to={'/jobs?job=' + encodeURIComponent(receipt.job_id)}>
          {kind === 'files' ? (isActiveJob({ status }) ? 'Publication job' : 'Latest file sync job') : 'View job'}
        </Link>
        {job.data?.last_error && <span>{job.data.last_error}</span>}
      </div>}
    </>,
  }
}

export function RepositorySyncAction({ repo, kind, label }: { repo: Repository; kind: SyncPolicy['kind']; label: string }) {
  const sync = useRepositorySync(repo, kind)
  return <div>
    <Button variant="secondary" size="sm" disabled={sync.busy} onClick={sync.start}>{label}</Button>
    {sync.feedback}
  </div>
}
