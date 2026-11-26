import { Link } from 'react-router-dom'
import { DataTable } from '@/components/shared/DataTable'
import { documentPath, formatDate } from '@/components/shared/format'
import type { DocumentSummary, Repository } from '@/types/domain'

export function DocumentOriginTag({ document }: { document: DocumentSummary }) {
  return document.origin && <span className="provenance">Origin: {document.origin.repository_name}</span>
}

export function DocumentTable({ documents, repositories }: { documents: DocumentSummary[]; repositories: Repository[] }) {
  const names = new Map(repositories.map((repo) => [repo.id, repo.name]))
  return <DataTable label="Documents" columns={['Title', 'Scope', 'Provenance', 'Version', 'Updated']}
    empty="No documents in this view. Create a document to add Markdown knowledge."
    rows={documents.map((doc) => ({ key: doc.id, cells: [
      <Link className="text-link" to={documentPath(doc.project_id, doc.id)}>{doc.title}</Link>,
      doc.repository_id ? `Repository / ${names.get(doc.repository_id) ?? 'Unknown repository'}` : 'Project',
      doc.origin ? <DocumentOriginTag document={doc} /> : '—', 'v' + doc.version, formatDate(doc.updated_at),
    ] }))} />
}
