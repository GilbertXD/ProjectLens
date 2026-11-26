import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { LensMark } from '@/components/brand/LensMark'
import { DataTable } from '@/components/shared/DataTable'
import { ProjectDialog } from '@/components/shared/ProjectDialog'
import { formatDate, projectPath } from '@/components/shared/format'
import { Button } from '@/components/ui/Button'
import { ConfirmDialog } from '@/components/ui/ConfirmDialog'
import { Pagination } from '@/components/ui/Pagination'
import { Select } from '@/components/ui/Select'
import { StatusBadge } from '@/components/ui/StatusBadge'
import { ApiContent, ApiFailure } from '@/api/ApiFeedback'
import { useArchiveProject, useDeleteProject, useProjects } from '@/api/queries'
import type { Project, ProjectStatus } from '@/types/domain'

const limit = 8

export function ProjectsPage() {
  const navigate = useNavigate()
  const [filter, setFilter] = useState<ProjectStatus | ''>('')
  const [pageOffset, setOffset] = useState(0)
  const [editor, setEditor] = useState<Project | 'create'>()
  const [deleting, setDeleting] = useState<Project>()
  const query = useProjects({ status: filter || undefined, limit, offset: pageOffset })
  const archive = useArchiveProject()
  const remove = useDeleteProject()
  const page = query.data

  useEffect(() => {
    if (page && page.offset > 0 && page.offset >= page.total) {
      setOffset(Math.max(0, Math.ceil(page.total / page.limit) - 1) * page.limit)
    }
  }, [page])

  return (
    <main id="main-content" tabIndex={-1} className="home-container global-home">
      <header className="home-heading">
        <div><h1>Projects</h1><p>See beyond the code.</p></div>
        <LensMark className="home-heading__mark" />
      </header>
      <div className="list-toolbar">
        <Select label="Project status" value={filter} onChange={(event) => { setFilter(event.target.value as ProjectStatus | ''); setOffset(0) }}>
          <option value="">All</option><option value="active">Active</option><option value="archived">Archived</option>
        </Select>
        <Button onClick={() => setEditor('create')}>Create project</Button>
      </div>
      {archive.error && <ApiFailure error={archive.error} />}
      <ApiContent query={query} loading="Loading projects…">
        <DataTable label="Projects" columns={['Name', 'Description', 'Status', 'Updated', 'Actions']}
          empty="No projects match this status."
          rows={page?.items.map((project) => ({
            key: project.id,
            cells: [
              <div className="stacked-cell"><strong>{project.name}</strong><code className={project.slug.length > 24 ? 'slug--long' : undefined}>{project.slug}</code></div>,
              project.description || '—', <StatusBadge status={project.status} />,
              <time dateTime={project.updated_at}>{formatDate(project.updated_at)}</time>,
              <div className="inline-actions">
                <Link className="text-link" to={projectPath(project.id)} aria-label={`Open ${project.name}`}>Open</Link>
                <Button size="sm" variant="ghost" onClick={() => setEditor(project)}>Edit</Button>
                <Button size="sm" variant="ghost" disabled={archive.isPending} onClick={() => archive.mutate(project)}>
                  {project.status === 'active' ? 'Archive' : 'Restore'}
                </Button>
                <Button size="sm" variant="ghost" onClick={() => setDeleting(project)}>Delete</Button>
              </div>,
            ],
          })) ?? []}
        />
        {page && <Pagination total={page.total} limit={page.limit} offset={page.offset} onOffsetChange={setOffset} />}
      </ApiContent>
      {editor && <ProjectDialog project={editor === 'create' ? undefined : editor} onDismiss={() => setEditor(undefined)}
        onSaved={(id) => { setEditor(undefined); if (editor === 'create') void navigate(projectPath(id)) }} />}
      <ConfirmDialog open={Boolean(deleting)} title="Delete project" description={deleting?.name}
        impact={<>
          <p>This permanently deletes the project, its repository associations, snapshots, indexed files, imported history and activity, sync policies, and all project and repository documents. Active project jobs receive cancellation requests.</p>
          {remove.error && <ApiFailure error={remove.error} />}
        </>}
        confirmLabel="Delete project" pending={remove.isPending} onCancel={() => { if (!remove.isPending) { setDeleting(undefined); remove.reset() } }} onConfirm={() => {
          if (deleting) remove.mutate(deleting.id, { onSuccess: () => setDeleting(undefined) })
        }} />
    </main>
  )
}
