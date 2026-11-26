import { useEffect, useRef } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useConnection } from './ConnectionProvider'
import { canPoll, isActiveJob, jobOptions, jobPollInterval } from './queries'
import type { Job } from '@/types/domain'

export function useJobPoller(id?: string, onSettled?: (job: Job) => void) {
  const { connection: api } = useConnection()
  const notified = useRef<Job | undefined>(undefined)
  const query = useQuery({
    ...jobOptions(api, id ?? ''),
    enabled: Boolean(id),
    refetchInterval: ({ state }) => {
      if (!canPoll(state.error)) return false
      return !state.data || isActiveJob(state.data) ? jobPollInterval : false
    },
    refetchOnWindowFocus: ({ state }) => !state.data || isActiveJob(state.data),
    refetchOnReconnect: ({ state }) => !state.data || isActiveJob(state.data),
  })
  useEffect(() => {
    const job = query.data
    if (!id || !job || isActiveJob(job)) {
      notified.current = undefined
      return
    }
    if (onSettled && notified.current !== job) {
      notified.current = job
      onSettled(job)
    }
  }, [id, onSettled, query.data])
  return query
}
