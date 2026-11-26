import { useState } from 'react'
import type { FormEvent } from 'react'
import { ApiContent, ApiFailure } from '@/api/ApiFeedback'
import { useConnection } from '@/api/ConnectionProvider'
import { parseApiBaseUrl } from '@/api/connection'
import { useDiagnostics } from '@/api/queries'
import { KeyValueList } from '@/components/shared/KeyValueList'
import { PageHeading, SectionRule } from '@/components/shared/SectionRule'
import { formatDate } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { StatusBadge } from '@/components/ui/StatusBadge'

export function SettingsPage() {
  const { connection, setBaseUrl, setToken } = useConnection()
  const { health, readiness, ping } = useDiagnostics()
  const [base, setBase] = useState(connection.apiBaseUrl)
  const [token, setDraftToken] = useState(connection.getToken)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  function applyBase(value: string) {
    const origin = parseApiBaseUrl(value)
    if (origin === undefined) {
      setError('Enter an HTTP or HTTPS origin without credentials, a path, query, or fragment.')
      return
    }
    setBaseUrl(origin)
    setBase(origin)
    if (origin !== connection.apiBaseUrl) setDraftToken('')
    setError('')
    setNotice('Connection settings saved.')
    ping.reset()
  }

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    applyBase(base)
  }

  function applyToken(value: string) {
    setToken(value)
    setDraftToken(value.trim())
    ping.reset()
    setNotice(value.trim() ? 'Bearer token set for this tab.' : 'Bearer token cleared.')
  }

  return <main id="main-content" tabIndex={-1} className="home-container global-page">
    <PageHeading title="Settings" description="Configure the API connection and inspect service diagnostics." />
    <SectionRule title="API connection">
      <form className="settings-form" onSubmit={save}>
        <Input label="API base URL" value={base} error={error} placeholder="Same-origin proxy"
          onChange={(event) => { setBase(event.target.value); setNotice(''); setError('') }} />
        <div className="inline-actions">
          <Button type="submit">Save connection</Button>
          <Button variant="secondary" onClick={() => applyBase('')}>Reset API base URL</Button>
        </div>
      </form>
      <form className="settings-form" onSubmit={(event) => { event.preventDefault(); applyToken(token) }}>
        <Input label="Bearer token" type="password" autoComplete="off" value={token}
          aria-describedby="token-notice" onChange={(event) => { setDraftToken(event.target.value); setNotice(''); ping.reset() }} />
        <p className="muted" id="token-notice">Session only. Reloading this tab or changing the API origin clears the token.</p>
        <div className="inline-actions">
          <Button type="submit" variant="secondary">Set token</Button>
          <Button variant="ghost" onClick={() => applyToken('')}>Clear token</Button>
          <Button variant="secondary" disabled={ping.isPending} onClick={() => { setNotice(''); ping.mutate() }}>Test connection</Button>
          {(ping.data || notice) && <span role="status">{ping.data ? 'API returned ' + ping.data.message + '.' : notice}</span>}
        </div>
        {ping.error && <ApiFailure error={ping.error} />}
      </form>
    </SectionRule>
    <div className="split-pane">
      <SectionRule title="Service health" actions={<code>/healthz</code>}>
        <ApiContent query={health} loading="Checking service health…">
          {health.data && <KeyValueList entries={[
            ['Status', <StatusBadge status={health.data.status} />], ['Service', health.data.service],
            ['Environment', health.data.env], ['Version', health.data.version], ['Uptime', health.data.uptime],
            ['Checked', formatDate(health.data.timestamp)],
          ]} />}
        </ApiContent>
      </SectionRule>
      <SectionRule title="Readiness" actions={<code>/readyz</code>}>
        <ApiContent query={readiness} loading="Checking readiness…">
          {readiness.data && <KeyValueList entries={[
            ['Status', <StatusBadge status={readiness.data.status} />],
            ['PostgreSQL', <StatusBadge status={readiness.data.checks.postgres} />],
            ['Redis', <StatusBadge status={readiness.data.checks.redis} />],
          ]} />}
        </ApiContent>
      </SectionRule>
    </div>
    <SectionRule title="API contract">
      <div className="inline-actions">
        <a className="text-link" href={connection.apiBaseUrl + '/openapi.json'} target="_blank" rel="noreferrer">OpenAPI JSON</a>
        <a className="text-link" href={connection.apiBaseUrl + '/openapi.yaml'} target="_blank" rel="noreferrer">OpenAPI YAML</a>
      </div>
    </SectionRule>
  </main>
}
