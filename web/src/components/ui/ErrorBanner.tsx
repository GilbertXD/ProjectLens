import type { ReactNode } from 'react'

type ErrorBannerProps = {
  message: string
  code?: string
  requestId?: string
  action?: ReactNode
}

export function ErrorBanner({ message, code, requestId, action }: ErrorBannerProps) {
  return (
    <div className="error-banner">
      <div role="alert">
        <p>{message}</p>
        {(code || requestId) && (
          <dl className="error-banner__details">
            {code && <div><dt>Error code</dt><dd><code>{code}</code></dd></div>}
            {requestId && <div><dt>Request ID</dt><dd><code>{requestId}</code></dd></div>}
          </dl>
        )}
      </div>
      {action}
    </div>
  )
}
