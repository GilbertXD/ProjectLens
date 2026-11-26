import type { ReactNode } from 'react'
import { Button } from '@/components/ui/Button'
import { ErrorBanner } from '@/components/ui/ErrorBanner'
import { ApiError } from './client'

const recoveryHints: Readonly<Partial<Record<number, string>>> = {
  403: 'Confirm that the resource is allowed by server policy, or ask a ProjectLens administrator for access.',
  404: 'Check the address or return to the list and open the resource again.',
  409: 'Review the current state and resolve the conflict before retrying.',
  413: 'Reduce the request size and try again.',
  429: 'Wait for the rate limit to reset before trying again.',
  502: 'Check that the upstream service is reachable, then try again.',
  503: 'Wait a moment and retry. If the issue continues, check Readiness in Settings.',
  504: 'Check the current state before retrying. Narrow the request if possible.',
}

export function ApiFailure({ error, hint, retry }: { error: Error; hint?: string; retry?: () => void }) {
  const apiError = error instanceof ApiError ? error : undefined
  const unauthorized = apiError?.status === 401
  const recovery = hint ?? (apiError?.status === 409 && apiError.code === 'version_conflict'
    ? 'Load the latest version and review your changes before retrying.'
    : apiError && recoveryHints[apiError.status])

  return <ErrorBanner message={recovery ? error.message + ' ' + recovery : error.message}
    code={apiError?.code}
    requestId={apiError?.requestId}
    action={(unauthorized || retry) && <div className="inline-actions">
      {unauthorized && <p>Set a valid Bearer token in Settings before retrying.</p>}
      {retry && <Button variant="secondary" size="sm" onClick={retry}>Retry</Button>}
    </div>} />
}

export function ApiContent({ query, loading, children }: {
  query: { isPending: boolean; data: unknown; error: Error | null; refetch: () => unknown }; loading: string; children: ReactNode
}) {
  if (query.isPending) return <p className="rule-message" role="status">{loading}</p>
  if (query.error) return <>
    <ApiFailure error={query.error} retry={() => { void query.refetch() }} />
    {query.data !== undefined && children}
  </>
  return children
}
