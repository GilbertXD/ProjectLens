import { lazy, Suspense } from 'react'
import { Link, Navigate, NavLink, Outlet, useParams } from 'react-router-dom'
import type { RouteObject } from 'react-router-dom'
import { ConsoleLayout } from '@/App'
import { PageHeading } from '@/components/shared/SectionRule'
import { StatusBadge } from '@/components/ui/StatusBadge'
import { ApiContent } from '@/api/ApiFeedback'
import { useProjectQuery } from '@/api/queries'
import { JobsPage } from '@/pages/global/JobsPage'
import { ProjectsPage } from '@/pages/global/ProjectsPage'
import { SettingsPage } from '@/pages/global/SettingsPage'
import { DocumentsPage } from '@/pages/project/DocumentsPage'
import { OverviewPage } from '@/pages/project/OverviewPage'
import { RepositoriesPage } from '@/pages/project/RepositoriesPage'
import { RepositoryDetailPage } from '@/pages/project/RepositoryDetailPage'
import { SearchPage } from '@/pages/project/SearchPage'

const ActivityPage = lazy(() => import('@/pages/project/ActivityPage').then(({ ActivityPage }) => ({ default: ActivityPage })))
const DocumentDetailPage = lazy(() => import('@/pages/project/DocumentDetailPage')
  .then(({ DocumentDetailPage }) => ({ default: DocumentDetailPage })))

const workspaceSections = [
  { path: 'overview', title: 'Overview', element: <OverviewPage /> },
  { path: 'repositories', title: 'Repositories', element: <RepositoriesPage /> },
  { path: 'search', title: 'Search', element: <SearchPage /> },
  { path: 'documents', title: 'Documents', element: <DocumentsPage /> },
  { path: 'activity', title: 'Activity', element: <ActivityPage /> },
]

function ProjectLayout() {
  const { projectId } = useParams()
  const query = useProjectQuery(projectId ?? '')
  const project = query.data

  if (!project || query.error) return <main id="main-content" tabIndex={-1} className="home-container global-page">
    <ApiContent query={query} loading="Loading project…">{null}</ApiContent>
    <Link className="text-link" to="/">Return to Projects</Link>
  </main>

  return (
    <div className="workspace">
      <aside className="workspace-rail">
        <Link className="workspace-rail__back" to="/">All projects</Link>
        <div className="workspace-rail__project">
          <strong>{project.name}</strong><code>{project.slug}</code>
          <StatusBadge status={project.status} />
        </div>
        <nav className="workspace-nav" aria-label="Project navigation">
          {workspaceSections.map(({ path, title }) => <NavLink key={path} to={path}>{title}</NavLink>)}
        </nav>
      </aside>
      <main id="main-content" tabIndex={-1} className="workspace-main" key={project.id}>
        <Suspense fallback={<p className="rule-message" role="status">Loading workspace view…</p>}>
          <Outlet context={project} />
        </Suspense>
      </main>
    </div>
  )
}

function MissingPage({ title = 'Page not found' }: { title?: string }) {
  return (
    <main id="main-content" tabIndex={-1} className="home-container global-page">
      <PageHeading title={title} description="Check the address or return to Projects." />
      <Link className="text-link" to="/">Return to Projects</Link>
    </main>
  )
}

export const routes: RouteObject[] = [
  {
    element: <ConsoleLayout />,
    children: [
      { index: true, element: <ProjectsPage /> },
      { path: 'jobs', element: <JobsPage /> },
      { path: 'settings', element: <SettingsPage /> },
      {
        path: 'projects/:projectId',
        element: <ProjectLayout />,
        children: [
          { index: true, element: <Navigate to="overview" replace /> },
          ...workspaceSections.map(({ path, element }) => ({ path, element })),
          { path: 'repositories/:repoId/*', element: <RepositoryDetailPage /> },
          { path: 'documents/:docId', element: <DocumentDetailPage /> },
          { path: '*', element: <PageHeading title="Workspace page not found" description="Choose a section in the project navigation." /> },
        ],
      },
      { path: '*', element: <MissingPage /> },
    ],
  },
]
