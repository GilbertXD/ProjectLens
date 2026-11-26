import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { App } from '@/App'
import { routes } from '../router'

function renderConsole(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(<App router={router} />)
  return router
}

describe('Global navigation', () => {
  it('renders the Projects masthead and navigates to Jobs and Settings', async () => {
    const user = userEvent.setup()
    renderConsole('/')
    const nav = within(screen.getByRole('navigation', { name: 'Global navigation' }))
    expect(screen.getByRole('heading', { name: 'Projects' })).toBeVisible()
    expect(nav.getByRole('link', { name: 'Projects' })).toHaveAttribute('aria-current', 'page')
    expect(screen.queryByRole('navigation', { name: 'Project navigation' })).toBeNull()

    await user.click(nav.getByRole('link', { name: 'Jobs' }))
    expect(screen.getByRole('heading', { name: 'Jobs' })).toBeVisible()
    await user.click(nav.getByRole('link', { name: 'Settings' }))
    expect(screen.getByRole('heading', { name: 'Settings' })).toBeVisible()
    expect(nav.getByRole('link', { name: 'Settings' })).toHaveAttribute('aria-current', 'page')
  })

  it('offers a way back from an unknown route', () => {
    renderConsole('/missing')
    expect(screen.getByRole('heading', { name: 'Page not found' })).toBeVisible()
    expect(screen.getByRole('link', { name: 'Return to Projects' })).toHaveAttribute('href', '/')
  })
})

describe('Project workspace', () => {
  it('opens Overview at the project root and keeps both navigation levels visible', async () => {
    const user = userEvent.setup()
    const router = renderConsole('/projects/project-1')
    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeVisible()
    expect(router.state.location.pathname).toBe('/projects/project-1/overview')
    expect(screen.getByRole('navigation', { name: 'Global navigation' })).toBeVisible()

    const nav = within(screen.getByRole('navigation', { name: 'Project navigation' }))
    expect(nav.getByRole('link', { name: 'Overview' })).toHaveAttribute('aria-current', 'page')
    await user.click(nav.getByRole('link', { name: 'Search' }))
    expect(screen.getByRole('heading', { name: 'Search' })).toBeVisible()
    expect(router.state.location.pathname).toBe('/projects/project-1/search')
  })

  it.each([
    ['repositories', 'Repositories', 'Repositories'],
    ['repositories/repo-1', 'Repository', 'Repositories'],
    ['repositories/repo-1/commits/123', 'Repository', 'Repositories'],
    ['documents', 'Documents', 'Documents'],
    ['documents/doc-1', 'Document', 'Documents'],
    ['activity', 'Activity', 'Activity'],
  ])('renders %s with its project section selected', (path, heading, section) => {
    renderConsole(`/projects/project-1/${path}`)
    expect(screen.getByRole('heading', { name: heading })).toBeVisible()
    const nav = within(screen.getByRole('navigation', { name: 'Project navigation' }))
    expect(nav.getByRole('link', { name: section })).toHaveAttribute('aria-current', 'page')
  })
})
