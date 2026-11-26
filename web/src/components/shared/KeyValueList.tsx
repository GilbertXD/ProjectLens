import type { ReactNode } from 'react'

export function KeyValueList({ entries }: { entries: [string, ReactNode][] }) {
  return <dl className="key-values">{entries.map(([label, value]) => (
    <div key={label}><dt>{label}</dt><dd>{value}</dd></div>
  ))}</dl>
}
