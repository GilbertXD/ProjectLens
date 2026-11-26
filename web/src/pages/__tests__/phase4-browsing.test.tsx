import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from '@/App'
import { projectPath, repositoryPath } from '@/components/shared/format'
import { fixtureIds, repositoryPreviews, searchPath } from '@/test/fixtures'
import { routes } from '@/routes/router'
import { json, mockApi, problem } from './apiFixture'

const project = projectPath(fixtureIds.core)
const repo = repositoryPath(fixtureIds.core, fixtureIds.api)
const github = repositoryPath(fixtureIds.core, fixtureIds.gin)
const apiRepo = '/api/v1' + repo
const apiGitHub = '/api/v1' + github
const apiProject = '/api/v1' + project
const fixture = repositoryPreviews.find(({ repository }) => repository.id === fixtureIds.api)!
const history = fixture.history!
const [head, base] = history.items
const binaryPath = 'assets/index #1.bin'

function openConsole(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(<App router={router} />)
  return { router, user: userEvent.setup() }
}

function requests(fetch: ReturnType<typeof mockApi>, path: string, method = 'GET') {
  return fetch.mock.calls.flatMap(([input, init]) => {
    const url = new URL(input, 'http://localhost')
    return url.pathname === path && (init?.method ?? 'GET') === method ? [url.searchParams] : []
  })
}

afterEach(() => vi.unstubAllGlobals())

describe('Live knowledge browsing', () => {
  it('opens an active snapshot deep link in a read-only, highlighted CodeMirror viewer', async () => {
    const fetch = mockApi()
    const { user } = openConsole(repo + '/files?' + new URLSearchParams({ path: searchPath, line: '5' }))
    const code = await screen.findByLabelText('Source code')
    expect(code).toHaveAttribute('contenteditable', 'false')
    expect(code).toHaveAttribute('aria-readonly', 'true')
    expect(code.querySelector('#L5.cm-line-focus')).toHaveTextContent('type Scope string')
    await waitFor(() => expect(code.querySelector('.cm-line span[class]')).toHaveTextContent('package'))
    expect(code.closest('.cm-editor')?.querySelector('.cm-lineNumbers')).toHaveTextContent('5')
    expect(screen.getByText('Active snapshot', { exact: true })).toBeVisible()
    expect(screen.getByText(/SHA-256/)).toBeVisible()
    const [initialFiles] = requests(fetch, apiRepo + '/files')
    expect(initialFiles.has('path_prefix')).toBe(false)
    expect(initialFiles.has('q')).toBe(false)
    expect(requests(fetch, apiRepo + '/files/content')[0].get('path')).toBe(searchPath)

    await user.type(screen.getByLabelText('Search files'), 'scope')
    await user.type(screen.getByLabelText('Path prefix'), 'internal/search/')
    await user.click(screen.getByRole('button', { name: 'Filter files' }))
    expect(await screen.findByRole('link', { name: 'scope_test.go' })).toBeVisible()
    const filtered = requests(fetch, apiRepo + '/files').at(-1)!
    expect(filtered.get('q')).toBe('scope')
    expect(filtered.get('path_prefix')).toBe('internal/search/')
  })

  it('pins a ref to its commit, opens a binary file, and returns to the active snapshot', async () => {
    const treeEntry = { path: binaryPath, blob_oid: head.hash, mode: '100755', size: 31, is_executable: true }
    const fetch = mockApi((url) => {
      if (url.pathname === apiRepo + '/refs') {
        return json({ items: [{ type: 'tag', name: 'v1', ref: 'refs/tags/v1', object_hash: base.hash, commit_hash: head.hash }],
          limit: 50, offset: 0, total: 1, default_branch: 'main' })
      }
      if (url.pathname === apiRepo + '/commits/' + head.hash + '/tree') {
        return json({ items: [treeEntry], limit: 30, offset: 0, total: 1, commit_hash: head.hash, tree_hash: head.tree_hash })
      }
      if (url.pathname === apiRepo + '/commits/' + head.hash + '/file' && url.searchParams.get('path') === binaryPath) {
        return json({ ...treeEntry, commit_hash: head.hash, sha256: 'abc12345'.repeat(8), line_count: 0, binary: true })
      }
    })
    const { user, router } = openConsole(repo + '/files')
    await user.selectOptions(await screen.findByLabelText('Git ref'), await screen.findByRole('option', { name: 'v1 (tag)' }))
    await user.click(await screen.findByRole('link', { name: 'index #1.bin' }))
    expect(await screen.findByText('Binary file. Text preview is unavailable.')).toBeVisible()
    expect(screen.getByText('Historical commit')).toBeVisible()
    expect(screen.getByText(head.hash)).toBeVisible()
    expect(screen.getByText(/Executable/)).toHaveTextContent('100755')
    expect(screen.queryByLabelText('Source code')).toBeNull()
    expect(new URLSearchParams(router.state.location.search).get('commit')).toBe(head.hash)
    expect(requests(fetch, apiRepo + '/refs')[0].has('type')).toBe(false)
    expect(requests(fetch, apiRepo + '/commits/' + head.hash + '/tree')[0].has('path_prefix')).toBe(false)
    expect([...requests(fetch, apiRepo + '/commits/' + head.hash + '/file')[0]]).toEqual([['path', binaryPath]])
    await user.click(screen.getByRole('link', { name: 'Return to active snapshot' }))
    expect(await screen.findByText('Active snapshot', { exact: true })).toBeVisible()
    expect(router.state.location.search).toBe('')
  })

  it('offers publication when the repository has no active snapshot', async () => {
    mockApi((url) => url.pathname === apiRepo + '/snapshots/active' ? problem(404, 'not_found', 'not found')
      : url.pathname === apiRepo + '/files' ? json({ items: [], limit: 30, offset: 0, total: 0 }) : undefined)
    openConsole(repo + '/files')
    expect(await screen.findByRole('heading', { name: 'No active snapshot' })).toBeVisible()
    expect(screen.getByRole('link', { name: 'Publish snapshot' })).toHaveAttribute('href', repo + '/snapshots')
  })

  it('follows renamed paths in paged file history and validates blame bounds before requesting them', async () => {
    const oldPath = 'internal/search/legacy scope.go'
    const fetch = mockApi((url) => {
      if (url.pathname === apiRepo + '/files/history' && url.searchParams.get('follow') === 'true') {
        return json({ items: [{ ...base, path: oldPath, old_path: 'internal/legacy.go', change_type: 'R' }],
          has_more: url.searchParams.get('offset') === '0', limit: 20, offset: Number(url.searchParams.get('offset')),
          shallow: true, history_complete: false, truncated: true })
      }
      if (url.pathname === apiRepo + '/files/blame' && url.searchParams.has('end_line')) {
        return json({ lines: fixture.blame[searchPath].slice(1, 3), start_line: 2, end_line: 3, total_lines: 10000,
          shallow: true, boundary_reached: true, truncated: true, history_complete: false })
      }
    })
    const { user } = openConsole(repo + '/files?' + new URLSearchParams({ path: searchPath }))
    await user.click(await screen.findByRole('button', { name: 'File history' }))
    await screen.findByRole('table', { name: 'File history' })
    expect(requests(fetch, apiRepo + '/files/history')[0].has('follow')).toBe(false)
    await user.click(screen.getByRole('checkbox', { name: 'Follow renames' }))
    expect(await screen.findByText('History incomplete')).toBeVisible()
    expect(screen.getByText('Shallow history')).toBeVisible()
    const renamed = screen.getByRole('link', { name: base.subject })
    const href = new URL(renamed.getAttribute('href')!, 'http://localhost')
    expect(href.searchParams.get('path')).toBe(oldPath)
    expect(href.searchParams.get('commit')).toBe(base.hash)
    const historyPanel = screen.getByRole('heading', { name: 'File history' }).closest('section')!
    await user.click(within(historyPanel).getByRole('button', { name: 'Next' }))
    expect(await screen.findByText('21–21')).toBeVisible()
    expect(requests(fetch, apiRepo + '/files/history').at(-1)?.get('follow')).toBe('true')

    await user.click(screen.getByRole('button', { name: 'Blame' }))
    expect(await screen.findByRole('region', { name: 'Blame and source code' })).toHaveAttribute('tabindex', '0')
    const [unbounded] = requests(fetch, apiRepo + '/files/blame')
    expect(unbounded.has('start_line')).toBe(false)
    expect(unbounded.has('end_line')).toBe(false)
    expect(unbounded.get('ref')).toBe(head.hash)
    await user.type(screen.getByLabelText('Start line'), '2')
    await user.click(screen.getByRole('button', { name: 'Apply range' }))
    expect(screen.getByText('Enter both line bounds for a range of at most 2,000 lines.')).toBeVisible()
    expect(requests(fetch, apiRepo + '/files/blame')).toHaveLength(1)
    await user.type(screen.getByLabelText('End line'), '3')
    await user.click(screen.getByRole('button', { name: 'Apply range' }))
    expect(await screen.findByText(/Blame output was truncated/)).toBeVisible()
    expect(screen.getByText('Shallow history')).toBeVisible()
    expect(screen.getByText('History boundary reached')).toBeVisible()
    const bounded = requests(fetch, apiRepo + '/files/blame').at(-1)!
    expect([bounded.get('start_line'), bounded.get('end_line')]).toEqual(['2', '3'])
  })

  it('loads imported commit details and compares paged file changes with a truncated patch notice', async () => {
    const fetch = mockApi((url) => {
      if (url.pathname === apiRepo + '/commits') return json({ ...history, window_status: 'partial', total: 125 })
      if (url.pathname === apiRepo + '/compare') return json({ ...fixture.comparisons[0], total: 21, patch_truncated: true })
    })
    const { user } = openConsole(repo + '/commits')
    const commit = await screen.findByRole('link', { name: head.subject })
    expect(screen.getByText('Partial', { exact: true })).toBeVisible()
    expect(screen.getByText('125 imported')).toBeVisible()
    expect(screen.getByText('Window limit 2000')).toBeVisible()
    expect(requests(fetch, apiRepo + '/commits')[0].has('ref')).toBe(false)
    await user.click(commit)
    expect(await screen.findByRole('table', { name: 'Changed files' })).toHaveTextContent(searchPath)
    expect(await screen.findByRole('link', { name: 'Browse files at commit' })).toHaveAttribute('href', repo + '/files?commit=' + head.hash)
    expect(requests(fetch, apiRepo + '/commits/' + head.hash)).toHaveLength(1)
    expect(requests(fetch, apiRepo + '/commits/' + head.hash + '/files')[0].get('offset')).toBe('0')
    await user.click(screen.getByRole('button', { name: 'Compare commits' }))
    await user.click(screen.getByRole('button', { name: 'Show diff' }))
    const diff = await screen.findByLabelText('Unified diff')
    expect(diff.querySelector('.change-add')).toHaveTextContent('case')
    expect(diff.querySelector('.change-delete')).toHaveTextContent('case')
    expect(diff.querySelector('.diff-hunk')).toHaveTextContent('@@')
    expect(screen.getByText('The diff patch was truncated.')).toBeVisible()
    const compare = screen.getByRole('heading', { name: 'Compare commits' }).closest('section')!
    await user.click(within(compare).getByRole('button', { name: 'Next' }))
    await waitFor(() => expect(requests(fetch, apiRepo + '/compare').at(-1)?.get('offset')).toBe('20'))
    expect(requests(fetch, apiRepo + '/compare')[0].get('base')).toBe(base.hash)
    expect(requests(fetch, apiRepo + '/compare')[0].get('head')).toBe(head.hash)
  })

  it('syncs the selected commit ref and refreshes the imported window when the job succeeds', async () => {
    let refReads = 0
    const fetch = mockApi((url) => {
      if (url.pathname === apiRepo + '/commits' && url.searchParams.get('ref') === 'release/next') {
        refReads += 1
        return json({ ...history, ref: 'release/next', window_status: 'partial', total: refReads * 100 })
      }
    })
    const { user } = openConsole(repo + '/commits')
    await user.type(await screen.findByLabelText('Custom ref'), 'release/next')
    await user.click(screen.getByRole('button', { name: 'Load ref' }))
    expect(await screen.findByText('release/next', { selector: '.commit-window code' })).toBeVisible()
    expect(screen.getByText('100 imported')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Sync commits' }))
    expect(await screen.findByText('Succeeded', {}, { timeout: 3500 })).toBeVisible()
    const sync = fetch.mock.calls.find(([input, init]) => input.endsWith('/commits/sync') && init?.method === 'POST')
    expect(JSON.parse(String(sync?.[1]?.body))).toEqual({ ref: 'release/next' })
    expect(await screen.findByText('200 imported')).toBeVisible()
  })

  it('browses project and repository discussions with Markdown, review locations, PR commits and honest pagination', async () => {
    const fetch = mockApi((url) => {
      if (url.pathname === apiGitHub + '/pulls/4186/comments') {
        return json({ items: [{ id: 1, kind: 'review_comment', user: { login: 'reviewer' }, path: searchPath,
          line: 7, body: '**Please add a test.**' }], limit: 20, offset: 0 })
      }
      if (url.pathname === apiRepo + '/issues') return json({ items: null, limit: 20, offset: 0 })
    }, { additionalIssues: 19 })
    const { user } = openConsole(project + '/activity')
    expect(await screen.findByRole('link', { name: head.subject })).toHaveAttribute('href', repo + '/commits?sha=' + head.hash)
    await user.click(screen.getByRole('button', { name: 'Issues' }))
    expect(await screen.findByText('1–20')).toBeVisible()
    expect(screen.queryByText(/1–20 of/)).toBeNull()
    const issuePagination = within(screen.getByRole('navigation', { name: 'Pagination' }))
    await user.click(await screen.findByRole('button', { name: /Clarify middleware order/ }))
    expect(await screen.findByText(/A three-level example/)).toBeVisible()
    expect(requests(fetch, apiProject + '/issues')).toHaveLength(1)
    expect(requests(fetch, apiGitHub + '/issues/4182')).toHaveLength(1)
    expect(requests(fetch, apiGitHub + '/issues/4182/comments')).toHaveLength(1)

    await user.click(issuePagination.getByRole('button', { name: 'Next' }))
    expect(await screen.findByText('21–21')).toBeVisible()
    expect(screen.getByRole('button', { name: /Fixture issue 19$/ })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Previous' })).toBeEnabled()
    expect(requests(fetch, apiProject + '/issues').map((params) => [params.get('limit'), params.get('offset')]))
      .toEqual([['20', '0'], ['20', '20']])

    await user.selectOptions(screen.getByLabelText('Repository'), fixtureIds.api)
    expect(await screen.findByText('No imported GitHub activity for this selection.')).toBeVisible()
    expect(requests(fetch, apiRepo + '/issues')).toHaveLength(1)
    await user.selectOptions(screen.getByLabelText('Repository'), fixtureIds.gin)
    expect(await screen.findByRole('table', { name: 'Issues' })).toHaveTextContent('Clarify middleware order')
    expect(requests(fetch, apiGitHub + '/issues')).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: 'Pull requests' }))
    await user.click(await screen.findByRole('button', { name: /Add a nested middleware example/ }))
    expect(await screen.findByText('Please add a test.', { selector: 'strong' })).toBeVisible()
    expect(screen.getByText(searchPath + ':7')).toBeVisible()
    expect(await screen.findByRole('link', { name: /Document route group middleware/ })).toHaveAttribute('href',
      expect.stringContaining(github + '/commits?sha='))
    expect(requests(fetch, apiGitHub + '/pulls/4186')).toHaveLength(1)
    expect(requests(fetch, apiGitHub + '/pulls/4186/commits')).toHaveLength(1)

    await user.click(screen.getByRole('button', { name: 'Sync GitHub activity' }))
    expect(await screen.findByText('Succeeded', {}, { timeout: 3500 })).toBeVisible()
    expect(requests(fetch, apiGitHub + '/github-activity/sync', 'POST')).toHaveLength(1)
    await waitFor(() => expect(requests(fetch, apiGitHub + '/pulls')).toHaveLength(2))
    await user.selectOptions(screen.getByLabelText('Repository'), '')
    expect(await screen.findByRole('table', { name: 'Pull requests' })).toHaveTextContent('Add a nested middleware example')
    expect(requests(fetch, apiProject + '/pulls')).toHaveLength(1)
  })
})
