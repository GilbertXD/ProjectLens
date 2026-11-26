import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from '@/App'
import { jobPollInterval } from '@/api/queries'
import { documents, fixtureIds, jobs, projects, repositoryPreviews } from '@/test/fixtures'
import { routes } from '@/routes/router'
import { json, mockApi, problem } from './apiFixture'

const core = '/projects/' + fixtureIds.core
const projectApi = '/api/v1' + core
const repoApi = projectApi + '/repositories/' + fixtureIds.api
const dialog = () => within(screen.getByRole('dialog'))

function open(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(<App router={router} />)
  return { router, user: userEvent.setup() }
}

function request(fetch: ReturnType<typeof mockApi>, path: string, method = 'GET') {
  return fetch.mock.calls.find(([input, init]) => new URL(input, 'http://localhost').pathname === path && (init?.method ?? 'GET') === method)
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
  localStorage.clear()
})

describe('Phase 3 HTTP integration', () => {
  it('handles project CRUD and preserves a draft after a slug conflict', async () => {
    const fetch = mockApi()
    const { router, user } = open('/')
    await user.click(screen.getByRole('button', { name: 'Create project' }))
    await user.type(dialog().getByLabelText('Name'), 'Shipping Console')
    await user.type(dialog().getByLabelText('Slug'), projects[0].slug)
    await user.click(dialog().getByRole('button', { name: 'Create project' }))
    expect(await dialog().findByRole('alert')).toHaveTextContent('Choose a different slug.')
    expect(dialog().getByText('project-conflict')).toBeVisible()
    expect(dialog().getByLabelText('Name')).toHaveValue('Shipping Console')
    await user.clear(dialog().getByLabelText('Slug'))
    await user.type(dialog().getByLabelText('Slug'), 'shipping-console')
    await user.click(dialog().getByRole('button', { name: 'Create project' }))
    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeVisible()
    const id = /^\/projects\/([\da-f-]{36})\/overview$/.exec(router.state.location.pathname)?.[1]
    expect(id).toBeTruthy()
    await waitFor(() => expect(request(fetch, '/api/v1/projects/' + id)).toBeDefined())

    await user.click(screen.getByRole('button', { name: 'Edit project' }))
    await user.type(dialog().getByLabelText('Description'), 'Release coordination')
    await user.click(dialog().getByRole('button', { name: 'Save project' }))
    expect(await screen.findByText('Release coordination')).toBeVisible()
    expect(JSON.parse(String(request(fetch, '/api/v1/projects/' + id, 'PATCH')?.[1]?.body)))
      .toMatchObject({ description: 'Release coordination' })

    await user.click(screen.getByRole('link', { name: 'All projects' }))
    const row = await screen.findByRole('row', { name: /Shipping Console/ })
    await user.click(within(row).getByRole('button', { name: 'Delete' }))
    expect(dialog().getByText(/all project and repository documents/)).toBeVisible()
    await user.click(dialog().getByRole('button', { name: 'Delete project' }))
    await waitFor(() => expect(screen.queryByRole('row', { name: /Shipping Console/ })).toBeNull())
    expect(request(fetch, '/api/v1/projects/' + id, 'DELETE')).toBeDefined()
  })

  it('uses API totals and requests pages and filters from the server', async () => {
    const fetch = mockApi((url) => {
      if (url.pathname !== '/api/v1/projects') return
      const offset = Number(url.searchParams.get('offset'))
      return json({ items: [{ ...projects[0], name: offset ? 'Page two' : 'Page one' }], total: 9, limit: 8, offset })
    })
    const { user } = open('/')
    expect(await screen.findByText('1–8 of 9')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(await screen.findByText('Page two')).toBeVisible()
    expect(screen.getByText('9–9 of 9')).toBeVisible()
    await user.selectOptions(screen.getByLabelText('Project status'), 'active')
    expect(await screen.findByText('Page one')).toBeVisible()
    expect(fetch.mock.calls.at(-1)?.[0]).toBe('/api/v1/projects?status=active&limit=8&offset=0')
  })

  it('renders public diagnostics, structured ping errors and recovery, then authenticates in memory', async () => {
    const fetch = mockApi((url, _method, init) => {
      if (url.pathname === '/api/v1/ping' && !new Headers(init?.headers).has('Authorization')) {
        return problem(401, 'unauthorized', 'Bearer token required', { 'X-Request-ID': 'ping-401' })
      }
    })
    const { user } = open('/settings')
    expect(await screen.findByText('48h 26m')).toBeVisible()
    const readiness = within(screen.getByRole('heading', { name: 'Readiness' }).closest('section')!)
    expect(within(readiness.getByText('PostgreSQL').parentElement!).getByText('Unavailable')).toBeVisible()
    expect(within(readiness.getByText('Redis').parentElement!).getByText('Degraded')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText('ping-401')).toBeVisible()
    expect(screen.getByText('Bearer token required')).toBeVisible()
    expect(screen.getByText('Set a valid Bearer token in Settings before retrying.')).toBeVisible()
    expect(screen.getByRole('link', { name: 'Check connection settings' })).toHaveAttribute('href', '/settings')

    await user.type(screen.getByLabelText('Bearer token'), 'memory-only')
    await user.click(screen.getByRole('button', { name: 'Set token' }))
    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText('API returned pong.')).toBeVisible()
    const pings = fetch.mock.calls.filter(([path]) => path === '/api/v1/ping')
    expect(new Headers(pings.at(-1)?.[1]?.headers).get('Authorization')).toBe('Bearer memory-only')
    expect(screen.queryByRole('link', { name: 'Check connection settings' })).toBeNull()
    expect(localStorage.length).toBe(0)
    expect(sessionStorage.length).toBe(0)
    await user.click(screen.getByRole('button', { name: 'Clear token' }))
    expect(screen.getByLabelText('Bearer token')).toHaveValue('')
    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText('Bearer token required')).toBeVisible()
  })

  it('switches backend data and resets back to the same-origin proxy', async () => {
    const fetch = mockApi((url) => {
      if (url.pathname === '/api/v1/projects' && url.hostname === 'other.example') {
        return json({ items: [{ ...projects[0], name: 'Other backend project' }], total: 1, limit: 8, offset: 0 })
      }
    })
    const { user } = open('/')
    expect(await screen.findByText(projects[0].name)).toBeVisible()
    const nav = within(screen.getByRole('navigation', { name: 'Global navigation' }))
    await user.click(nav.getByRole('link', { name: 'Settings' }))
    await user.type(screen.getByLabelText('API base URL'), 'https://other.example')
    await user.click(screen.getByRole('button', { name: 'Save connection' }))
    await user.click(nav.getByRole('link', { name: 'Projects' }))
    expect(await screen.findByText('Other backend project')).toBeVisible()
    expect(screen.queryByText(projects[0].name)).toBeNull()
    expect(fetch.mock.calls.some(([url]) => url === 'https://other.example/api/v1/projects?limit=8&offset=0')).toBe(true)

    await user.click(nav.getByRole('link', { name: 'Settings' }))
    await user.click(screen.getByRole('button', { name: 'Reset API base URL' }))
    expect(screen.getByRole('link', { name: 'OpenAPI YAML' })).toHaveAttribute('href', '/openapi.yaml')
    expect(localStorage.getItem('projectlens.apiOrigin')).toBeNull()
    await user.click(nav.getByRole('link', { name: 'Projects' }))
    expect(await screen.findByText(projects[0].name)).toBeVisible()
    expect(screen.queryByText('Other backend project')).toBeNull()
  })

  it('associates, syncs, writes a policy, refreshes metadata, and confirms disassociation', async () => {
    const fetch = mockApi()
    const { user } = open(core + '/repositories')
    await user.click(await screen.findByRole('button', { name: 'Associate repository' }))
    await user.selectOptions(dialog().getByLabelText('Source type'), 'github')
    await user.type(dialog().getByLabelText('GitHub owner'), 'example')
    await user.type(dialog().getByLabelText('GitHub repository'), 'reference')
    await user.selectOptions(dialog().getByLabelText('Role'), 'reference')
    await user.click(dialog().getByRole('button', { name: 'Associate repository' }))
    const association = await screen.findByRole('row', { name: /example\/reference/ })
    expect(JSON.parse(String(request(fetch, projectApi + '/repositories', 'POST')?.[1]?.body))).toMatchObject({
      role: 'reference', source_type: 'github', source: 'https://github.com/example/reference',
    })
    await user.selectOptions(screen.getByLabelText('Role'), 'reference')
    await user.selectOptions(screen.getByLabelText('Source type'), 'github')
    const filtered = within(await screen.findByRole('row', { name: /example\/reference/ }))
    expect(fetch.mock.calls.some(([url]) => url === projectApi + '/repositories?role=reference&source_type=github&limit=10&offset=0')).toBe(true)
    const repoPath = within(association).getByRole('link', { name: 'example/reference' }).getAttribute('href')
    const apiPath = '/api/v1' + repoPath
    await user.click(filtered.getByRole('button', { name: 'Sync files' }))
    expect(await filtered.findByText('Job queued')).toBeVisible()
    expect(await filtered.findByText('Running')).toBeVisible()
    expect(await filtered.findByText('Succeeded', {}, { timeout: 3500 })).toBeVisible()
    expect(request(fetch, apiPath + '/sync', 'POST')).toBeDefined()

    await user.click(filtered.getByRole('link', { name: 'example/reference' }))
    const tabs = within(await screen.findByRole('navigation', { name: 'Repository sections' }))
    await user.click(tabs.getByRole('link', { name: 'Settings' }))
    await user.click(screen.getByRole('button', { name: 'Refresh metadata' }))
    expect(await screen.findByText('Metadata refreshed.')).toBeVisible()
    expect(request(fetch, apiPath + '/refresh-metadata', 'POST')).toBeDefined()
    await user.click(await screen.findByLabelText('Enable files sync'))
    const interval = screen.getByLabelText('Files interval (seconds)')
    await user.clear(interval)
    await user.type(interval, '900')
    await user.click(screen.getByRole('button', { name: 'Save files policy' }))
    expect(await screen.findByText('Saved')).toBeVisible()
    expect(JSON.parse(String(request(fetch, apiPath + '/sync-policy/files', 'PUT')?.[1]?.body))).toEqual({
      enabled: true, interval_seconds: 900,
    })
    await user.click(screen.getByRole('button', { name: 'Sync commits' }))
    await user.click(screen.getByRole('button', { name: 'Sync GitHub activity' }))
    expect(request(fetch, apiPath + '/commits/sync', 'POST')).toBeDefined()
    expect(request(fetch, apiPath + '/github-activity/sync', 'POST')).toBeDefined()

    await user.click(screen.getByRole('button', { name: 'Disassociate repository' }))
    expect(dialog().getByText(/documents survive as project-level documents with read-only origin/)).toBeVisible()
    expect(request(fetch, apiPath, 'DELETE')).toBeUndefined()
    await user.click(dialog().getByRole('button', { name: 'Disassociate repository' }))
    expect(await screen.findByRole('heading', { name: 'Repositories', level: 1 })).toBeVisible()
    expect(request(fetch, apiPath, 'DELETE')).toBeDefined()
    await screen.findByRole('table', { name: 'Repositories' })
    expect(screen.queryByRole('link', { name: 'example/reference' })).toBeNull()
  }, 8500)

  it('shows allowlist and competing-ref errors and identifies reused sync jobs', async () => {
    let attempts = 0
    const fetch = mockApi((url, method) => {
      if (url.pathname === projectApi + '/repositories' && method === 'POST') {
        return problem(403, 'forbidden', 'path outside allowed roots')
      }
      if (url.pathname === repoApi + '/sync' && method === 'POST') {
        if (++attempts === 1) return problem(409, 'conflict', 'a sync for a different ref is already active')
        return json({ job_id: jobs[0].id, status: 'pending', poll_url: '/api/v1/jobs/' + jobs[0].id })
      }
    })
    const { user } = open(core + '/repositories')
    await user.click(await screen.findByRole('button', { name: 'Associate repository' }))
    await user.type(dialog().getByLabelText('Local Git path'), '/outside/repo')
    await user.click(dialog().getByRole('button', { name: 'Associate repository' }))
    expect(await dialog().findByRole('alert')).toHaveTextContent('allowed Git root')
    expect(dialog().getByLabelText('Local Git path')).toHaveValue('/outside/repo')
    await user.click(dialog().getByRole('button', { name: 'Cancel' }))
    const repository = within(await screen.findByRole('row', { name: /projectlens-api/ }))
    await user.click(repository.getByRole('button', { name: 'Sync files' }))
    expect(await repository.findByRole('alert')).toHaveTextContent('a sync for a different ref is already active')
    await user.click(repository.getByRole('button', { name: 'Sync files' }))
    expect(await repository.findByText('Reused active job')).toBeVisible()
    await waitFor(() => expect(request(fetch, '/api/v1/jobs/' + jobs[0].id)).toBeDefined())
  })

  it('aggregates overview totals, per-repository schedules and jobs across global pages', async () => {
    const other = { ...jobs[0], payload: { project_id: 'another-project' } }
    const projectJob = { ...jobs[2], payload: { project_id: fixtureIds.core } }
    const fetch = mockApi((url) => {
      if (url.pathname === projectApi + '/repositories') {
        return json({ items: repositoryPreviews.map(({ repository }) => repository), total: 12, limit: 5, offset: 0 })
      }
      if (url.pathname === projectApi + '/documents') {
        return json({ items: documents, total: 20, limit: 5, offset: 0 })
      }
      if (url.pathname === '/api/v1/jobs') {
        const offset = Number(url.searchParams.get('offset'))
        return json({ items: offset ? [projectJob] : [other], total: 2, limit: 1, offset })
      }
    })
    open(core + '/overview')
    expect(await screen.findByRole('link', { name: 'Indexer migration notes' })).toBeVisible()
    expect(await screen.findByRole('link', { name: 'View all (12)' })).toHaveAttribute('href', core + '/repositories')
    expect(await screen.findByRole('link', { name: 'View all (20)' })).toHaveAttribute('href', core + '/documents')
    const policies = within(await screen.findByRole('table', { name: 'Sync policies' }))
    expect(policies.getByRole('link', { name: 'projectlens-api' })).toBeVisible()
    expect(request(fetch, repoApi + '/sync-policy')).toBeDefined()
    const recent = within(await screen.findByRole('table', { name: 'Recent project jobs' }))
    expect(recent.getByRole('link', { name: 'Sync files' })).toHaveAttribute('href', '/jobs?job=' + projectJob.id)
    expect(recent.queryByRole('link', { name: 'Scan repository' })).toBeNull()
    expect(fetch.mock.calls.some(([url]) => url === '/api/v1/jobs?limit=50&offset=1')).toBe(true)
  })

  it('polls a jobs page while active jobs exist and stops when they finish', async () => {
    vi.useFakeTimers()
    let reads = 0
    mockApi((url) => {
      if (url.pathname === '/api/v1/jobs') {
        const status = ++reads === 1 ? 'pending' : 'succeeded'
        return json({ items: [{ ...jobs[0], status }], total: 1, limit: 10, offset: 0 })
      }
    })
    open('/jobs')
    const tick = async (ms: number) => {
      await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
    }
    await tick(5)
    expect(screen.getByText('Pending', { selector: '.status-badge' })).toBeVisible()
    await tick(jobPollInterval)
    expect(screen.getByText('Succeeded', { selector: '.status-badge' })).toBeVisible()
    await tick(jobPollInterval * 2)
    expect(reads).toBe(2)
  })

  it('filters jobs on the server, loads the selected payload, and cancels it', async () => {
    const fetch = mockApi()
    const { user } = open('/jobs')
    await user.selectOptions(screen.getByLabelText('Job status'), 'pending')
    await user.selectOptions(screen.getByLabelText('Job type'), 'repository.scan')
    await user.click(await screen.findByRole('button', { name: /Scan repository/ }))
    expect(await screen.findByText(/"project_id"/)).toBeVisible()
    const detailPath = '/api/v1/jobs/' + jobs[0].id
    expect(request(fetch, detailPath)).toBeDefined()
    expect(fetch.mock.calls.some(([url]) => url === '/api/v1/jobs?status=pending&type=repository.scan&limit=10&offset=0')).toBe(true)
    await user.click(screen.getByRole('button', { name: 'Cancel job' }))
    expect(await screen.findByText('No jobs match these filters.')).toBeVisible()
    const detail = within(screen.getByRole('heading', { name: 'Job details' }).closest('section')!)
    expect(detail.getByText('Cancelled')).toBeVisible()
    expect(detail.queryByRole('button', { name: 'Cancel job' })).toBeNull()
    expect(request(fetch, detailPath + '/cancel', 'POST')).toBeDefined()
  })
})
