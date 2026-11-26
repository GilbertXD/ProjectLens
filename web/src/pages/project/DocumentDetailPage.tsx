import { useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { SafeMarkdown } from '@/components/shared/SafeMarkdown'
import { PageHeading } from '@/components/shared/SectionRule'
import { formatDate, projectPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { ErrorBanner } from '@/components/ui/ErrorBanner'
import { Input } from '@/components/ui/Input'
import { Textarea } from '@/components/ui/Textarea'
import { useMockConsole } from '@/mocks/MockConsoleProvider'
import type { Document } from '@/types/domain'
import { DocumentOriginTag } from './DocumentTable'
import { useProject } from './workspace'

function DocumentEditor({ document }: { document: Document }) {
  const store = useMockConsole()
  const navigate = useNavigate()
  const back = projectPath(document.project_id) + '/documents'
  const [title, setTitle] = useState(document.title)
  const [content, setContent] = useState(document.content)
  const [version, setVersion] = useState(document.version)
  const [deleteVersion, setDeleteVersion] = useState<number>()
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [view, setView] = useState<'split' | 'editor' | 'preview'>('split')
  const conflict = version !== document.version
  const repository = store.repositories.find(({ repository }) => repository.id === document.repository_id)?.repository

  function save(event: FormEvent<HTMLFormElement>, targetVersion: number) {
    event.preventDefault()
    if (!title.trim()) return setError('Enter a document title.')
    if (targetVersion !== document.version) return
    store.saveDocument(document.id, { title: title.trim(), content }, targetVersion)
    setTitle(title.trim())
    setVersion(targetVersion + 1)
    setError('')
    setNotice('Document saved.')
  }

  return <>
    <p className="back-link"><Link className="text-link" to={back}>All documents</Link></p>
    <PageHeading title={document.title} actions={<Button variant="danger" size="sm"
      onClick={() => setDeleteVersion(document.version)}>Delete document</Button>} />
    <div className="metadata">
      <span>{repository ? `Repository / ${repository.name}` : 'Project document'}</span>
      <code>v{document.version}</code><DocumentOriginTag document={document} />
      <span>Updated {formatDate(document.updated_at)}</span>
    </div>
    {conflict && <ErrorBanner
      message={`The document changed while you were editing. Your draft uses v${version}. The latest version is v${document.version}.`}
      code="409 version_conflict"
      action={<Button variant="secondary" size="sm" onClick={() => {
        setTitle(document.title); setContent(document.content); setVersion(document.version); setError('')
      }}>Reload latest version</Button>} />}
    {conflict && <details className="latest-document">
      <summary>Review latest version</summary>
      <h2>{document.title}</h2><SafeMarkdown content={document.content} />
    </details>}
    <form className="document-form" onSubmit={(event) => save(event, conflict ? document.version : version)}>
      <Input required label="Title" value={title} error={error} onChange={(event) => { setTitle(event.target.value); setNotice('') }} />
      <div className="inline-actions" aria-label="Editor view" role="group">
        {(['split', 'editor', 'preview'] as const).map((mode) => <Button key={mode} size="sm"
          variant={view === mode ? 'primary' : 'secondary'} aria-pressed={view === mode} onClick={() => setView(mode)}>
          {{ split: 'Split view', editor: 'Editor', preview: 'Preview' }[mode]}
        </Button>)}
      </div>
      <div className={view === 'split' ? 'split-pane document-panes' : 'document-panes'}>
        {view !== 'preview' && <Textarea label="Markdown editor" className="markdown-editor" value={content}
          onChange={(event) => { setContent(event.target.value); setNotice('') }} />}
        {view !== 'editor' && <section className="document-preview" aria-label="Markdown preview">
          <h2>Markdown preview</h2>{content ? <SafeMarkdown content={content} /> : <p className="muted">Add Markdown to preview it.</p>}
        </section>}
      </div>
      <div className="inline-actions">
        <Button type="submit">{conflict ? 'Retry with latest version' : 'Save document'}</Button>
        <Button variant="secondary" disabled={conflict} onClick={() => {
          store.simulateDocumentConflict(document.id, version); setNotice('')
        }}>Simulate version conflict</Button>
        <span role="status">{notice}</span>
      </div>
    </form>
    <ConfirmDialog open={deleteVersion !== undefined} title="Delete document" description={document.title}
      impact="This permanently deletes the Markdown document and removes it from search results."
      confirmLabel="Delete document" onCancel={() => setDeleteVersion(undefined)} onConfirm={() => {
        if (deleteVersion !== document.version) {
          setDeleteVersion(undefined)
          setNotice('The document version changed. Review it and confirm deletion again.')
          return
        }
        store.deleteDocument(document.id, deleteVersion)
        void navigate(back)
      }} />
  </>
}

export function DocumentDetailPage() {
  const { docId } = useParams()
  const project = useProject()
  const store = useMockConsole()
  const doc = store.documents.find((item) => item.project_id === project.id && item.id === docId)
  if (!doc) return <>
    <PageHeading title="Document not found" />
    <Link className="text-link" to={projectPath(project.id) + '/documents'}>All documents</Link>
  </>
  return <DocumentEditor key={doc.id} document={doc} />
}
