import { lazy, Suspense, useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ApiContent, ApiFailure } from '@/api/ApiFeedback'
import { ApiError } from '@/api/client'
import { documentMaxBytes } from '@/api/endpoints'
import { useDeleteDocument, useDocument, useRepository, useUpdateDocument } from '@/api/queries'
import { SafeMarkdown } from '@/components/shared/SafeMarkdown'
import { PageHeading } from '@/components/shared/SectionRule'
import { formatDate, projectPath, repositoryPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { ErrorBanner } from '@/components/ui/ErrorBanner'
import { Input } from '@/components/ui/Input'
import { StatusBadge } from '@/components/ui/StatusBadge'
import type { Document } from '@/types/domain'
import { DocumentOriginTag } from './DocumentTable'
import { useProject } from './workspace'

const MarkdownEditor = lazy(() => import('@/components/code/MarkdownEditor')
  .then(({ MarkdownEditor }) => ({ default: MarkdownEditor })))
const viewLabels = { split: 'Split view', editor: 'Editor', preview: 'Preview' }
type Draft = Pick<Document, 'title' | 'content' | 'version'>
type Conflict = {
  operation: 'save' | 'delete'
  error: ApiError
  latest?: Document
  refreshError?: Error
}

function isVersionConflict(error: unknown): error is ApiError {
  return error instanceof ApiError && error.status === 409 && ['version_conflict', 'conflict'].includes(error.code)
}

function DocumentOwnership({ document }: { document: Document }) {
  const repoId = document.scope === 'repository' ? document.repository_id ?? '' : ''
  const repository = useRepository(document.project_id, repoId)
  return <>
    <StatusBadge status={document.scope === 'repository' ? 'Repository' : 'Project'} />
    {repoId && <Link className="text-link" to={repositoryPath(document.project_id, repoId)}>
      {repository.data?.name ?? repoId}
    </Link>}
    <DocumentOriginTag document={document} />
  </>
}

function VersionConflict({ conflict, draft, busy, canSave, onRefresh, onReload, onOverwrite, onKeep }: {
  conflict: Conflict; draft: Draft; busy: boolean; canSave: boolean
  onRefresh: () => void; onReload: () => void; onOverwrite: (version: number) => void; onKeep: () => void
}) {
  const { operation, error, latest, refreshError } = conflict
  return <section className="document-conflict" aria-label="Version conflict">
    <ErrorBanner code={`409 ${error.code}`} requestId={error.requestId}
      message={operation === 'save' ? 'The document changed on the server. Your draft is still available.'
        : 'The document changed on the server. Review the latest version, then confirm deletion again.'} />
    {refreshError && <ApiFailure error={refreshError} hint="Could not load the latest version." retry={onRefresh} />}
    {!latest && !refreshError && <p role="status">Loading latest version…</p>}
    {latest && <>
      <div className={operation === 'save' ? 'split-pane conflict-versions' : 'conflict-versions'}>
        {operation === 'save' && <section>
          <h2>Your draft (from v{draft.version})</h2>
          <p className="conflict-title">{draft.title}</p>
          <SafeMarkdown content={draft.content} />
        </section>}
        <section>
          <h2>Latest server version (v{latest.version})</h2>
          <p className="conflict-title">{latest.title}</p>
          <SafeMarkdown content={latest.content} />
        </section>
      </div>
      {operation === 'save' && <div className="inline-actions">
        <Button variant="secondary" disabled={busy} onClick={onReload}>Reload latest version</Button>
        <Button disabled={busy || !canSave} onClick={() => onOverwrite(latest.version)}>Save my draft over v{latest.version}</Button>
      </div>}
    </>}
    {operation === 'delete' && !busy && <div>
      <Button variant="secondary" onClick={onKeep}>Keep document</Button>
    </div>}
  </section>
}

function DocumentEditor({ document, refresh }: { document: Document; refresh: () => Promise<Document> }) {
  const navigate = useNavigate()
  const update = useUpdateDocument(document.project_id, document.id)
  const remove = useDeleteDocument(document.project_id, document.id)
  const back = projectPath(document.project_id) + '/documents'
  const [draft, setDraft] = useState<Draft>()
  const [deleteTarget, setDeleteTarget] = useState<Document>()
  const [conflict, setConflict] = useState<Conflict>()
  const [saved, setSaved] = useState(false)
  const [view, setView] = useState<keyof typeof viewLabels>('split')
  const current = draft ?? document
  const reviewed = conflict?.operation === 'delete' ? conflict.latest : document
  const busy = update.isPending || remove.isPending || Boolean(conflict && !conflict.latest && !conflict.refreshError)
  const bytes = new TextEncoder().encode(current.content).length
  const tooLarge = bytes > documentMaxBytes
  const canSave = Boolean(current.title.trim()) && !tooLarge

  function edit(fields: Partial<Pick<Draft, 'title' | 'content'>>) {
    setDraft((previous) => {
      const { title, content, version } = previous ?? document
      return { title, content, version, ...fields }
    })
    setSaved(false)
  }

  async function loadConflict(operation: Conflict['operation'], error: ApiError) {
    setConflict({ operation, error })
    try {
      setConflict({ operation, error, latest: await refresh() })
    } catch (failure) {
      setConflict({ operation, error, refreshError: failure as Error })
    }
  }

  async function save(version: number) {
    if (busy || !canSave) return
    remove.reset()
    setSaved(false)
    setDraft({ title: current.title, content: current.content, version: current.version })
    try {
      await update.mutateAsync({ input: { title: current.title.trim(), content: current.content }, version })
      setDraft(undefined)
      setConflict(undefined)
      setSaved(true)
    } catch (error) {
      if (isVersionConflict(error)) await loadConflict('save', error)
    }
  }

  async function deleteConfirmed() {
    if (busy || !deleteTarget) return
    update.reset()
    try {
      await remove.mutateAsync(deleteTarget.version)
      void navigate(back)
    } catch (error) {
      setDeleteTarget(undefined)
      if (isVersionConflict(error)) await loadConflict('delete', error)
    }
  }

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!conflict) void save(current.version)
  }
  const apiError = update.error ?? remove.error

  return <>
    <PageHeading title={document.title} actions={<Button variant="danger" size="sm"
      disabled={busy || !reviewed || conflict?.operation === 'save'}
      onClick={() => setDeleteTarget(reviewed)}>Delete document</Button>} />
    <div className="metadata">
      <DocumentOwnership document={document} />
      <code>v{document.version}</code>
      <span>{document.content_bytes.toLocaleString('en')} bytes</span>
      <span>Updated {formatDate(document.updated_at)}</span>
    </div>
    {conflict && <VersionConflict conflict={conflict} draft={current} busy={busy} canSave={canSave}
      onRefresh={() => { void loadConflict(conflict.operation, conflict.error) }}
      onReload={() => {
        const latest = conflict.latest
        if (!latest) return
        setDraft({ title: latest.title, content: latest.content, version: latest.version })
        update.reset()
        setConflict(undefined)
      }} onOverwrite={(version) => { void save(version) }} onKeep={() => {
        remove.reset()
        setConflict(undefined)
      }} />}
    {apiError && !isVersionConflict(apiError) && <ApiFailure error={apiError} />}
    <form className="document-form" onSubmit={submit}>
      <Input required label="Title" value={current.title} disabled={busy}
        onChange={(event) => edit({ title: event.target.value })} />
      <div className="inline-actions" aria-label="Editor view" role="group">
        {(Object.keys(viewLabels) as (keyof typeof viewLabels)[]).map((mode) => <Button key={mode} size="sm"
          variant={view === mode ? 'primary' : 'secondary'} aria-pressed={view === mode} onClick={() => setView(mode)}>
          {viewLabels[mode]}
        </Button>)}
      </div>
      <div className={view === 'split' ? 'split-pane document-panes' : 'document-panes'}>
        <div hidden={view === 'preview'} inert={busy}>
          <Suspense fallback={<p role="status">Loading Markdown editor…</p>}>
            <MarkdownEditor aria-label="Markdown editor" value={current.content} onChange={(content) => edit({ content })} />
          </Suspense>
        </div>
        {view !== 'editor' && <section className="document-preview" aria-label="Markdown preview">
          <h2>Markdown preview</h2>{current.content ? <SafeMarkdown content={current.content} /> : <p className="muted">Add Markdown to preview it.</p>}
        </section>}
      </div>
      {tooLarge && <p className="field-error" role="alert">Markdown must be at most 1 MiB.</p>}
      <div className="inline-actions">
        <Button type="submit" disabled={busy || Boolean(conflict) || !canSave}>
          {update.isPending ? 'Saving…' : 'Save document'}
        </Button>
        <span className="muted">{bytes.toLocaleString('en')} / {documentMaxBytes.toLocaleString('en')} bytes in draft</span>
        <span role="status">{saved ? 'Document saved.' : ''}</span>
      </div>
    </form>
    {deleteTarget && <ConfirmDialog open title="Delete document" description={`${deleteTarget.title} (v${deleteTarget.version})`}
      impact="This permanently deletes the Markdown document and removes it from search results."
      confirmLabel="Delete document" pending={remove.isPending}
      onCancel={() => { if (!remove.isPending) setDeleteTarget(undefined) }}
      onConfirm={() => { void deleteConfirmed() }} />}
  </>
}

export function DocumentDetailPage() {
  const { docId = '' } = useParams()
  const project = useProject()
  const query = useDocument(project.id, docId)
  return <>
    <p className="back-link"><Link className="text-link" to={projectPath(project.id) + '/documents'}>All documents</Link></p>
    {query.data
      ? <DocumentEditor key={project.id + '/' + docId} document={query.data} refresh={async () => {
        const result = await query.refetch({ throwOnError: true })
        return result.data!
      }} />
      : <ApiContent query={query} loading="Loading document…">{null}</ApiContent>}
  </>
}
