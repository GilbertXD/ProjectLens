import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Link, NavLink, Outlet, RouterProvider, useMatch } from 'react-router-dom'
import type { RouterProviderProps } from 'react-router-dom'
import { LensMark } from '@/components/brand/LensMark'
import { MockConsoleProvider } from '@/mocks/MockConsoleProvider'

const queryClient = new QueryClient()

export function App({ router }: Pick<RouterProviderProps, 'router'>) {
  return (
    <QueryClientProvider client={queryClient}>
      <MockConsoleProvider>
        <RouterProvider router={router} />
      </MockConsoleProvider>
    </QueryClientProvider>
  )
}

export function ConsoleLayout() {
  const inWorkspace = useMatch('/projects/:projectId/*')

  return (
    <>
      <a className="skip-link" href="#main-content">Skip to content</a>
      <header className="masthead">
        <div className={`masthead__inner ${inWorkspace ? '' : 'home-container'}`.trim()}>
          <Link to="/" className="masthead__brand">
            <LensMark width={32} height={32} />
            <span>ProjectLens</span>
          </Link>
          <nav className="global-nav" aria-label="Global navigation">
            <NavLink to="/" end className={inWorkspace ? 'within-projects' : undefined}>Projects</NavLink>
            <NavLink to="/jobs">Jobs</NavLink>
            <NavLink to="/settings">Settings</NavLink>
          </nav>
        </div>
      </header>
      <Outlet />
    </>
  )
}
