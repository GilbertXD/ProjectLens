import type { ReactNode } from 'react'
import { Button } from '@/components/ui/Button'
import { ErrorBanner } from '@/components/ui/ErrorBanner'
import { ApiError } from './client'

export function ApiFailure({ error, hint, retry }: { error: Error; hint?: string; retry?: () => void }) {
  return <ErrorBanner message={hint ? error.message + ' ' + hint : error.message}
    code={error instanceof ApiError ? error.code : undefined}
    requestId={error instanceof ApiError ? error.requestId : undefined}
    action={retry && <Button variant="secondary" size="sm" onClick={retry}>Retry</Button>} />
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
