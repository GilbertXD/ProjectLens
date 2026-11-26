import type { ReactNode } from 'react'

export function SectionRule({ title, actions, children }: { title: string; actions?: ReactNode; children: ReactNode }) {
  return <section className="section-rule">
    <div className="section-heading"><h2>{title}</h2>{actions}</div>
    {children}
  </section>
}

export function PageHeading({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  return <header className="page-heading heading-actions">
    <div><h1>{title}</h1>{description && <p>{description}</p>}</div>
    {actions}
  </header>
}
