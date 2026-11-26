import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter } from 'react-router-dom'
import { afterEach, expect, it, vi } from 'vitest'
import { App } from '@/App'
import { routes } from '@/routes/router'
import { json, mockApi, problem } from './apiFixture'

afterEach(() => vi.unstubAllGlobals())

it.each([
  [403, 'forbidden', 'ask a ProjectLens administrator for access'],
  [429, 'rate_limited', 'Wait for the rate limit to reset'],
  [502, 'upstream_error', 'Check that the upstream service is reachable'],
  [503, 'service_unavailable', 'check Readiness in Settings'],
])('shows recovery guidance and diagnostics for HTTP %i', async (status, code, guidance) => {
  const requestId = 'projects-' + status
  let attempts = 0
  mockApi((url) => {
    if (url.pathname !== '/api/v1/projects') return
    if (++attempts === 1) return problem(status, code, 'Project list request failed.', { 'X-Request-ID': requestId })
    return json({ items: [], total: 0, offset: 0, limit: 8 })
  })

  const user = userEvent.setup()
  render(<App router={createMemoryRouter(routes)} />)

  expect(screen.getByRole('heading', { name: 'Projects' })).toBeVisible()
  expect(screen.getByRole('status')).toHaveTextContent('Loading projects…')
  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('Project list request failed.')
  expect(alert).toHaveTextContent(guidance)
  expect(within(alert).getByText(code)).toBeVisible()
  expect(within(alert).getByText(requestId)).toBeVisible()

  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('No projects match this status.')).toBeVisible()
  expect(screen.queryByRole('alert')).toBeNull()
})
