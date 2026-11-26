import { useState } from 'react'
import type { FormEvent } from 'react'
import { KeyValueList } from '@/components/shared/KeyValueList'
import { PageHeading, SectionRule } from '@/components/shared/SectionRule'
import { formatDate } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { StatusBadge } from '@/components/ui/StatusBadge'
import { health, pingResponse, readiness } from '@/mocks/fixtures'
import { useMockConsole } from '@/mocks/MockConsoleProvider'

function parseOrigin(value: string) {
  const input = value.trim()
  if (!input) return ''
  if (!/^https?:\/\/[^/?#\\\s]+\/?$/i.test(input)) return undefined
  try {
    const url = new URL(input)
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password
      || url.pathname !== '/' || url.href !== url.origin + '/') return undefined
    return url.origin
  } catch {
    return undefined
  }
}

export function SettingsPage() {
  const store = useMockConsole()
  const [base, setBase] = useState(store.apiOrigin)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [ping, setPing] = useState('')

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const origin = parseOrigin(base)
    if (origin === undefined) return setError('Enter an HTTP or HTTPS origin without credentials, a path, query, or fragment.')
    store.saveApiOrigin(origin)
    setBase(origin)
    setError('')
    setPing('')
    setNotice('Connection settings saved.')
  }

  return <main id="main-content" tabIndex={-1} className="home-container global-page">
    <PageHeading title="Settings" description="Configure the API connection and inspect service diagnostics." />
    <SectionRule title="API connection">
      <form className="settings-form" onSubmit={save}>
        <Input label="API base URL" value={base} error={error} placeholder="Same-origin proxy"
          onChange={(event) => { setBase(event.target.value); setNotice(''); setError('') }} />
        <Input label="Bearer token" type="password" autoComplete="off" value={store.token}
          aria-describedby="token-notice" onChange={(event) => { store.setToken(event.target.value); setPing('') }} />
        <p className="muted" id="token-notice">Session only. Reloading this tab or changing the API origin clears the token.</p>
        <div className="inline-actions">
          <Button type="submit">Save connection</Button>
          <Button variant="secondary" onClick={() => setPing('Preview API returned ' + pingResponse.message + '.')}>Test API ping</Button>
          <span role="status">{ping || notice}</span>
        </div>
      </form>
    </SectionRule>
    <div className="split-pane">
      <SectionRule title="Service health" actions={<code>/healthz</code>}>
        <KeyValueList entries={[
          ['Status', <StatusBadge status={health.status} />], ['Service', health.service],
          ['Environment', health.env], ['Version', health.version], ['Uptime', health.uptime],
          ['Checked', formatDate(health.timestamp)],
        ]} />
      </SectionRule>
      <SectionRule title="Readiness" actions={<code>/readyz</code>}>
        <KeyValueList entries={[
          ['Status', <StatusBadge status={readiness.status} />],
          ['PostgreSQL', <StatusBadge status={readiness.checks.postgres} />],
          ['Redis', <StatusBadge status={readiness.checks.redis} />],
        ]} />
      </SectionRule>
    </div>
    <SectionRule title="API contract">
      <div className="inline-actions">
        <a className="text-link" href={store.apiOrigin + '/openapi.json'} target="_blank" rel="noreferrer">OpenAPI JSON</a>
        <a className="text-link" href={store.apiOrigin + '/openapi.yaml'} target="_blank" rel="noreferrer">OpenAPI YAML</a>
      </div>
    </SectionRule>
  </main>
}
