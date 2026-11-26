import { Link, Navigate, NavLink, Outlet, useParams } from 'react-router-dom'
import type { RouteObject } from 'react-router-dom'
import { ConsoleLayout } from '@/App'
import { LensMark } from '@/components/brand/LensMark'

const workspaceSections = [
  { path: 'overview', title: 'Overview', description: 'Review project details, repositories, documents, and sync jobs.' },
  { path: 'repositories', title: 'Repositories', description: 'Manage repository associations and inspect files and Git history.' },
  { path: 'search', title: 'Search', description: 'Find code and documents within a selected scope.' },
  { path: 'documents', title: 'Documents', description: 'Read and manage project and repository Markdown documents.' },
  { path: 'activity', title: 'Activity', description: 'Browse commits, issues, and pull requests from project repositories.' },
]

function GlobalHome() {
  return (
    <main id="main-content" tabIndex={-1} className="home-container global-home">
      <header className="home-heading">
        <div>
          <h1>Projects</h1>
          <p>See beyond the code.</p>
        </div>
        <LensMark className="home-heading__mark" />
      </header>
      <div className="home-intro">
        <p>Organize repositories and documentation by project.</p>
      </div>
    </main>
  )
}

function PageHeading({ title, description }: { title: string; description: string }) {
  return (
    <header className="page-heading">
      <h1>{title}</h1>
      <p>{description}</p>
    </header>
  )
}

function GlobalPage({ title, description }: { title: string; description: string }) {
  return (
    <main id="main-content" tabIndex={-1} className="home-container global-page">
      <PageHeading title={title} description={description} />
    </main>
  )
}

function ProjectLayout() {
  const { projectId } = useParams()

  return (
    <div className="workspace">
      <aside className="workspace-rail">
        <Link className="workspace-rail__back" to="/">All projects</Link>
        <p className="workspace-rail__id"><code>{projectId}</code></p>
        <nav className="workspace-nav" aria-label="Project navigation">
          {workspaceSections.map(({ path, title }) => (
            <NavLink key={path} to={path}>{title}</NavLink>
          ))}
        </nav>
      </aside>
      <main id="main-content" tabIndex={-1} className="workspace-main">
        <Outlet />
      </main>
    </div>
  )
}

function NotFound() {
  return (
    <main id="main-content" tabIndex={-1} className="home-container global-page">
      <PageHeading title="Page not found" description="Check the address or return to Projects." />
      <Link to="/">Return to Projects</Link>
    </main>
  )
}

export const routes: RouteObject[] = [
  {
    element: <ConsoleLayout />,
    children: [
      { index: true, element: <GlobalHome /> },
      { path: 'jobs', element: <GlobalPage title="Jobs" description="Inspect sync jobs and check their progress." /> },
      { path: 'settings', element: <GlobalPage title="Settings" description="Manage the API connection and check service health." /> },
      {
        path: 'projects/:projectId',
        element: <ProjectLayout />,
        children: [
          { index: true, element: <Navigate to="overview" replace /> },
          ...workspaceSections.map(({ path, title, description }) => ({
            path,
            element: <PageHeading title={title} description={description} />,
          })),
          {
            path: 'repositories/:repoId/*',
            element: <PageHeading title="Repository" description="Inspect snapshots, files, and Git history for this repository." />,
          },
          {
            path: 'documents/:docId',
            element: <PageHeading title="Document" description="Read or edit this Markdown document." />,
          },
        ],
      },
      { path: '*', element: <NotFound /> },
    ],
  },
]
