type Tone = 'neutral' | 'healthy' | 'error' | 'warning'

const statuses: Record<string, { label: string; tone: Tone }> = {
  pending: { label: 'Pending', tone: 'neutral' },
  running: { label: 'Running', tone: 'warning' },
  succeeded: { label: 'Succeeded', tone: 'healthy' },
  failed: { label: 'Failed', tone: 'error' },
  cancelled: { label: 'Cancelled', tone: 'neutral' },
  active: { label: 'Active', tone: 'neutral' },
  staging: { label: 'Staging', tone: 'warning' },
  superseded: { label: 'Superseded', tone: 'neutral' },
  ok: { label: 'Healthy', tone: 'healthy' },
  unavailable: { label: 'Unavailable', tone: 'error' },
  degraded: { label: 'Degraded', tone: 'warning' },
  conflict: { label: 'Conflict', tone: 'warning' },
  primary: { label: 'Primary', tone: 'neutral' },
  reference: { label: 'Reference', tone: 'neutral' },
  local_git: { label: 'Local Git', tone: 'neutral' },
  github: { label: 'GitHub', tone: 'neutral' },
}

export function StatusBadge({ status }: { status: string }) {
  const entry = Object.hasOwn(statuses, status) ? statuses[status] : undefined

  return <span className={`status-badge status-badge--${entry?.tone ?? 'neutral'}`}>{entry?.label ?? status}</span>
}
