import { useState } from 'react'
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
import { useMockConsole } from '@/mocks/MockConsoleProvider'
import type { Project } from '@/types/domain'

const limit = 8

export function ProjectsPage() {
  const store = useMockConsole()
  const navigate = useNavigate()
  const [filter, setFilter] = useState('all')
  const [pageOffset, setOffset] = useState(0)
  const [editor, setEditor] = useState<Project | 'create'>()
  const [deleting, setDeleting] = useState<Project>()
  const projects = store.projects.filter(({ status }) => filter === 'all' || status === filter)
  const offset = Math.min(pageOffset, Math.max(0, Math.ceil(projects.length / limit) - 1) * limit)

  return (
    <main id="main-content" tabIndex={-1} className="home-container global-home">
      <header className="home-heading">
        <div><h1>Projects</h1><p>See beyond the code.</p></div>
        <LensMark className="home-heading__mark" />
      </header>
      <div className="list-toolbar">
        <Select label="Project status" value={filter} onChange={(event) => { setFilter(event.target.value); setOffset(0) }}>
          <option value="all">All</option><option value="active">Active</option><option value="archived">Archived</option>
        </Select>
        <Button onClick={() => setEditor('create')}>Create project</Button>
      </div>
      <DataTable label="Projects" columns={['Name', 'Description', 'Status', 'Updated', 'Actions']}
        empty="No projects match this status."
        rows={projects.slice(offset, offset + limit).map((project) => ({
          key: project.id,
          cells: [
            <div className="stacked-cell"><strong>{project.name}</strong><code>{project.slug}</code></div>,
            project.description || '—', <StatusBadge status={project.status} />,
            <time dateTime={project.updated_at}>{formatDate(project.updated_at)}</time>,
            <div className="inline-actions">
              <Link className="text-link" to={projectPath(project.id)} aria-label={`Open ${project.name}`}>Open</Link>
              <Button size="sm" variant="ghost" onClick={() => setEditor(project)}>Edit</Button>
              <Button size="sm" variant="ghost" onClick={() => store.toggleProjectArchive(project)}>
                {project.status === 'active' ? 'Archive' : 'Restore'}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setDeleting(project)}>Delete</Button>
            </div>,
          ],
        }))}
      />
      <Pagination total={projects.length} limit={limit} offset={offset} onOffsetChange={setOffset} />
      {editor && <ProjectDialog project={editor === 'create' ? undefined : editor} onDismiss={() => setEditor(undefined)}
        onSaved={(id) => { setEditor(undefined); if (editor === 'create') void navigate(projectPath(id)) }} />}
      <ConfirmDialog open={Boolean(deleting)} title="Delete project" description={deleting?.name}
        impact="This permanently deletes the project, its repository associations, snapshots, indexed files, imported activity, sync policies, and all project and repository documents. Active project jobs are cancelled."
        confirmLabel="Delete project" onCancel={() => setDeleting(undefined)} onConfirm={() => {
          if (deleting) store.deleteProject(deleting.id)
          setDeleting(undefined)
        }} />
    </main>
  )
}
