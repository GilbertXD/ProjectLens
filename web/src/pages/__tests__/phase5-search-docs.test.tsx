import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { EditorView } from '@codemirror/view'
import { createMemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from '@/App'
import { createConnectionStore } from '@/api/connection'
import { createDocument, deleteDocument, getDocument, replaceDocument, searchProject, searchRepository } from '@/api/endpoints'
import { documentPath, projectPath, repositoryPath } from '@/components/shared/format'
import { documents, fixtureIds, searchPath } from '@/test/fixtures'
import { routes } from '@/routes/router'
import type { SearchHit, SearchScope } from '@/types/domain'
import { json, mockApi, problem } from './apiFixture'

const projectId = fixtureIds.core
const core = projectPath(projectId)
const docsPath = '/api/v1' + core + '/documents'
const searchPathname = '/api/v1' + core + '/search'
const repo = repositoryPath(projectId, fixtureIds.api)
const connection = createConnectionStore().getSnapshot().connection

function openConsole(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  render(<App router={router} />)
  return { router, user: userEvent.setup() }
}

function requests(fetch: ReturnType<typeof mockApi>, pathname: string, method = 'GET') {
  return fetch.mock.calls.flatMap(([input, init]) => {
    const url = new URL(input, 'http://localhost')
    return url.pathname === pathname && (init?.method ?? 'GET') === method ? [{ url, init }] : []
  })
}

const dialog = () => within(screen.getByRole('dialog'))
const docHeading = (name: string) => screen.findByRole('heading', { name, level: 1 })

async function createDraft(user: ReturnType<typeof userEvent.setup>, title: string, markdown: string) {
  await user.click(await screen.findByRole('button', { name: 'Create document' }))
  await user.type(dialog().getByLabelText('Title'), title)
  await user.type(dialog().getByLabelText('Markdown'), markdown)
  await user.click(dialog().getByRole('button', { name: 'Create document' }))
  await docHeading(title)
}

afterEach(() => vi.unstubAllGlobals())

describe('Scoped search API', () => {
  it.each([
    ['project', {}],
    ['repository', { repository_id: fixtureIds.api, path_prefix: 'internal/' }],
    ['cross_repository', { repository_ids: fixtureIds.api + ',' + fixtureIds.gin, repository_role: 'primary',
      include_project_documents: 'true', path_prefix: 'internal/' }],
    ['all', { path_prefix: 'internal/' }],
  ] satisfies [SearchScope, Record<string, string>][])('serializes only the filters permitted for %s', async (scope, expected) => {
    const fetch = mockApi()
    await searchProject(connection, projectId, {
      q: '  search  ', scope, repository_id: fixtureIds.api, repository_ids: [fixtureIds.api, fixtureIds.gin],
      repository_role: 'primary', include_project_documents: true, source_type: ['local_git', 'document'],
      path_prefix: 'internal/', limit: 5, offset: 2,
    })
    expect(Object.fromEntries(requests(fetch, searchPathname)[0].url.searchParams)).toEqual({
      q: 'search', scope, source_type: 'local_git,document', limit: '5', offset: '2', ...expected,
    })
  })

  it('omits false project inclusion and lets a repository path define its search scope', async () => {
    const fetch = mockApi()
    await searchProject(connection, projectId, { q: 'search', scope: 'cross_repository', include_project_documents: false })
    expect(requests(fetch, searchPathname)[0].url.searchParams.has('include_project_documents')).toBe(false)
    await searchRepository(connection, projectId, fixtureIds.api, { q: 'search', source_type: ['local_git'], limit: 4 })
    expect(Object.fromEntries(requests(fetch, '/api/v1' + repo + '/search')[0].url.searchParams)).toEqual({
      q: 'search', source_type: 'local_git', limit: '4',
    })
    await expect(searchProject(connection, projectId, { q: 'search', scope: 'repository' })).rejects.toThrow('Choose a repository.')
    expect(requests(fetch, searchPathname)).toHaveLength(1)
  })

  it('uses body diagnostics when cross-origin headers are unavailable', async () => {
    mockApi((url) => url.pathname === searchPathname
      ? json({ items: [], total: 0, limit: 20, offset: 0, search_version: 9, cache_status: 'bypass' }) : undefined)
    await expect(searchProject(connection, projectId, { q: 'search', scope: 'project' })).resolves.toMatchObject({
      search_version: 9, cache_status: 'bypass',
    })
  })

  it('restores submitted filters across navigation and renders safe snippets and source links', async () => {
    const file: SearchHit = {
      kind: 'source_file', id: 'file', source_type: 'github', project_id: projectId, title: searchPath,
      snippet: '&lt;img src=x onerror=alert(1)&gt; <mark>search</mark>', score: 0.5, updated_at: documents[0].updated_at,
      location: { path: searchPath, line: 2, repository_id: fixtureIds.api, repository_name: 'projectlens-api',
        repository_role: 'primary', commit_hash: 'abcdefgh12345678', snapshot_version: 2,
        url: 'https://github.com/projectlens/projectlens/blob/main/' + searchPath, api_path: '/unused' },
    }
    const document: SearchHit = {
      ...file, id: fixtureIds.migration, kind: 'document', source_type: 'document', title: 'Indexer migration notes', score: 0.25,
      location: { document_id: fixtureIds.migration, document_version: 4, url: 'javascript:alert(1)', api_path: '/unused' },
    }
    const fetch = mockApi((url) => url.pathname === searchPathname
      ? json({ items: [file, document], total: 42, limit: 20, offset: Number(url.searchParams.get('offset')),
        search_version: 1, cache_status: 'miss' }, 200, { 'X-Search-Cache': 'hit', 'X-Search-Version': '42' }) : undefined)
    const { user, router } = openConsole(core + '/search')
    await user.type(await screen.findByLabelText('Query'), 'search')
    await user.click(screen.getByRole('button', { name: 'Search' }))
    expect(requests(fetch, searchPathname)).toHaveLength(0)
    await user.selectOptions(screen.getByLabelText('Search scope'), 'all')
    await user.click(screen.getByLabelText('GitHub'))
    await user.type(screen.getByLabelText('Path prefix'), 'internal/')
    await user.click(screen.getByRole('button', { name: 'Search' }))

    const sourceLink = await screen.findByRole('link', { name: searchPath + ':2' })
    const result = screen.getByRole('region', { name: 'Search results' })
    expect(within(result).getByText('X-Search-Cache').parentElement).toHaveTextContent('hit')
    expect(within(result).getByText('X-Search-Version').parentElement).toHaveTextContent('42')
    expect(result.querySelector('mark')).toHaveTextContent('search')
    expect(result).toHaveTextContent('<img src=x onerror=alert(1)>')
    expect(result.querySelector('img, [onerror]')).toBeNull()
    expect(within(result).getByText('GitHub')).toBeVisible()
    expect(within(result).getByText('Primary')).toBeVisible()
    expect(within(result).getByText('abcdefgh')).toBeVisible()
    expect(within(result).getByText('v2')).toBeVisible()
    expect(within(result).getByText('v4')).toBeVisible()
    expect(within(result).getByText('Score 0.5')).toBeVisible()
    expect(within(result).getByText('Project documents')).toBeVisible()
    expect(within(result).getByRole('link', { name: 'View on GitHub' })).toHaveAttribute('href', file.location.url)
    expect(sourceLink).toHaveAttribute('href', repo + '/files?' + new URLSearchParams({ path: searchPath, line: '2' }) + '#L2')

    await user.click(within(result).getByRole('button', { name: 'Next' }))
    await user.click(await screen.findByRole('link', { name: searchPath + ':2' }))
    expect((await screen.findByLabelText('Source code')).querySelector('#L2')).toHaveClass('cm-line-focus')
    await act(() => router.navigate(-1))
    expect(await screen.findByLabelText('Query')).toHaveValue('search')
    expect(screen.getByLabelText('Search scope')).toHaveValue('all')
    expect(screen.getByLabelText('GitHub')).toBeChecked()
    expect(screen.getByLabelText('Path prefix')).toHaveValue('internal/')
    expect(new URLSearchParams(router.state.location.search).get('offset')).toBe('20')
    await user.click(await screen.findByRole('link', { name: 'Indexer migration notes' }))
    await docHeading('Indexer migration notes')
    expect(router.state.location.pathname).toBe(documentPath(projectId, fixtureIds.migration))
    expect(screen.getByText('Origin: indexer-v1')).toBeVisible()
  })
})

describe('Live Markdown documents', () => {
  it('filters document ownership and preserves project provenance when saving', async () => {
    const fetch = mockApi()
    const { user } = openConsole(core + '/documents')
    expect(await screen.findByText('Origin: indexer-v1')).toBeVisible()
    const filter = screen.getByLabelText('Document scope')
    await user.selectOptions(filter, 'repository')
    expect(await screen.findByText('Repository / projectlens-api')).toBeVisible()
    expect(screen.queryByText('Origin: indexer-v1')).toBeNull()
    await user.selectOptions(filter, fixtureIds.api)
    await screen.findByRole('link', { name: 'Search route contract' })
    await user.selectOptions(filter, 'project')
    expect(await screen.findByText('Origin: indexer-v1')).toBeVisible()
    const filters = requests(fetch, docsPath).map(({ url }) => [url.searchParams.get('scope'), url.searchParams.get('repository_id')])
    expect(filters).toEqual([[null, null], ['repository', null], ['repository', fixtureIds.api], ['project', null]])
    await user.click(screen.getByRole('link', { name: 'Indexer migration notes' }))
    await docHeading('Indexer migration notes')
    await user.clear(screen.getByLabelText('Title'))
    await user.type(screen.getByLabelText('Title'), 'Retained migration notes')
    await user.click(screen.getByRole('button', { name: 'Save document' }))
    await docHeading('Retained migration notes')
    expect(screen.getByText('Origin: indexer-v1')).toBeVisible()
    const [{ init }] = requests(fetch, docsPath + '/' + fixtureIds.migration, 'PATCH')
    expect(new Headers(init?.headers).get('If-Match')).toBe('"4"')
    expect(JSON.parse(String(init?.body))).toEqual({ title: 'Retained migration notes', content: documents[2].content, version: 4 })
  })

  it.each([400, 413])('retains the project creation draft after HTTP %s and retries it', async (status) => {
    let rejected = false
    const fetch = mockApi((url, method) => {
      if (url.pathname !== docsPath || method !== 'POST' || rejected) return
      rejected = true
      return problem(status, 'invalid_input', 'The server rejected this draft.')
    })
    const { user } = openConsole(core + '/documents')
    await user.click(await screen.findByRole('button', { name: 'Create document' }))
    expect(dialog().getByLabelText('Document owner')).toHaveValue('')
    await user.type(dialog().getByLabelText('Title'), 'UTF-8 notes')
    await user.type(dialog().getByLabelText('Markdown'), 'é猫')
    expect(dialog().getByText('5 / 1,048,576 bytes')).toBeVisible()
    await user.click(dialog().getByRole('button', { name: 'Create document' }))
    expect(await dialog().findByRole('alert')).toHaveTextContent('The server rejected this draft.')
    expect(dialog().getByLabelText('Title')).toHaveValue('UTF-8 notes')
    expect(dialog().getByLabelText('Markdown')).toHaveValue('é猫')
    await user.click(dialog().getByRole('button', { name: 'Create document' }))
    await docHeading('UTF-8 notes')
    const posts = requests(fetch, docsPath, 'POST')
    expect(posts).toHaveLength(2)
    for (const { init } of posts) expect(JSON.parse(String(init?.body))).toEqual({ title: 'UTF-8 notes', content: 'é猫' })
  })

  it('uses the scoped create route and strips read-only fields even when passed an existing document', async () => {
    const fetch = mockApi()
    const input = { ...documents[2], repository_id: fixtureIds.api }
    const created = await createDocument(connection, projectId, input)
    const [{ init }] = requests(fetch, '/api/v1' + repo + '/documents', 'POST')
    expect(JSON.parse(String(init?.body))).toEqual({
      title: input.title, content: input.content, repository_id: fixtureIds.api,
    })
    const doc = await getDocument(connection, projectId, created.id)
    expect(doc).toMatchObject({ scope: 'repository', repository_id: fixtureIds.api, version: 1, etag: '"1"' })
    await expect(replaceDocument(connection, projectId, doc.id, { ...input, content: 'Replacement' }, 1)).resolves.toMatchObject({
      content: 'Replacement', version: 2, etag: '"2"',
    })
    const [put] = requests(fetch, docsPath + '/' + doc.id, 'PUT')
    expect(new Headers(put.init?.headers).get('If-Match')).toBe('"1"')
    expect(JSON.parse(String(put.init?.body))).toEqual({ title: input.title, content: 'Replacement', version: 1 })
    await deleteDocument(connection, projectId, doc.id)
    expect(new Headers(requests(fetch, docsPath + '/' + doc.id, 'DELETE')[0].init?.headers).has('If-Match')).toBe(false)
  })

  it.each(['reload', 'overwrite'])('preserves a stale draft and can %s after a save conflict', async (resolution) => {
    const fetch = mockApi(undefined, { conflictOn: 'PATCH' })
    const { user, router } = openConsole(core + '/documents')
    await createDraft(user, 'Original', 'Original **content**')
    const pathname = '/api/v1' + router.state.location.pathname
    const title = screen.getByLabelText('Title')
    await user.clear(title)
    await user.type(title, 'My draft title')
    const editor = await screen.findByRole('textbox', { name: 'Markdown editor' })
    const view = EditorView.findFromDOM(editor)!
    const end = view.state.doc.length
    const insert = ' with **changes**'
    act(() => view.dispatch({ changes: { from: end, insert }, selection: { anchor: end + insert.length } }))
    expect(view.state.selection.main.head).toBe(end + insert.length)
    const preview = within(screen.getByRole('region', { name: 'Markdown preview' }))
    expect(preview.getByText('changes', { selector: 'strong' })).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Save document' }))

    const conflict = await screen.findByRole('region', { name: 'Version conflict' })
    expect(await within(conflict).findByRole('heading', { name: 'Latest server version (v2)' })).toBeVisible()
    expect(within(conflict).getByRole('heading', { name: 'Your draft (from v1)' })).toBeVisible()
    expect(within(conflict).getByText('Original (server)')).toBeVisible()
    expect(within(conflict).getByText('editor', { selector: 'strong' })).toBeVisible()
    expect(title).toHaveValue('My draft title')
    expect(editor).toHaveTextContent('Original **content** with **changes**')
    const firstPatch = requests(fetch, pathname, 'PATCH')[0]
    expect(new Headers(firstPatch.init?.headers).get('If-Match')).toBe('"1"')
    expect(JSON.parse(String(firstPatch.init?.body))).toEqual({
      title: 'My draft title', content: 'Original **content** with **changes**', version: 1,
    })
    expect(requests(fetch, pathname)).not.toHaveLength(0)
    const patchIndex = fetch.mock.calls.findIndex(([, init]) => init === firstPatch.init)
    expect(fetch.mock.calls.slice(patchIndex + 1).some(([path, init]) => path.endsWith(pathname) && !init?.method)).toBe(true)
    if (resolution === 'reload') {
      await user.click(within(conflict).getByRole('button', { name: 'Reload latest version' }))
      expect(title).toHaveValue('Original (server)')
      expect(editor).toHaveTextContent('Changes from another **editor**.')
      expect(requests(fetch, pathname, 'PATCH')).toHaveLength(1)
    } else {
      await user.click(within(conflict).getByRole('button', { name: 'Save my draft over v2' }))
      await docHeading('My draft title')
      const secondPatch = requests(fetch, pathname, 'PATCH')[1]
      expect(new Headers(secondPatch.init?.headers).get('If-Match')).toBe('"2"')
      expect(JSON.parse(String(secondPatch.init?.body))).toEqual({
        title: 'My draft title', content: 'Original **content** with **changes**', version: 2,
      })
      expect(screen.getByText('v3')).toBeVisible()
    }
    expect(screen.queryByRole('region', { name: 'Version conflict' })).toBeNull()
  })

  it('fetches the updated document after a stale delete and requires a new confirmation', async () => {
    const fetch = mockApi(undefined, { conflictOn: 'DELETE' })
    const { user, router } = openConsole(core + '/documents')
    await createDraft(user, 'Retire me', 'Initial text')
    const pathname = '/api/v1' + router.state.location.pathname
    await user.click(screen.getByRole('button', { name: 'Delete document' }))
    expect(dialog().getByRole('button', { name: 'Cancel' })).toHaveFocus()
    expect(dialog().getByText('Retire me (v1)')).toBeVisible()
    await user.click(dialog().getByRole('button', { name: 'Delete document' }))
    expect(await screen.findByRole('heading', { name: 'Latest server version (v2)' })).toBeVisible()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByRole('alert')).toHaveTextContent('confirm deletion again')
    expect(requests(fetch, pathname, 'DELETE')).toHaveLength(1)

    await user.click(screen.getByRole('button', { name: 'Delete document' }))
    expect(dialog().getByText('Retire me (server) (v2)')).toBeVisible()
    await user.click(dialog().getByRole('button', { name: 'Delete document' }))
    await docHeading('Documents')
    await screen.findByRole('table', { name: 'Documents' })
    expect(screen.queryByRole('link', { name: /Retire me/ })).toBeNull()
    expect(requests(fetch, pathname, 'DELETE').map(({ init }) => new Headers(init?.headers).get('If-Match'))).toEqual(['"1"', '"2"'])
  })

  it('supports server pagination and prevents unsafe Markdown preview content', async () => {
    const fetch = mockApi((url, method) => {
      if (url.pathname === docsPath && method === 'GET') {
        const offset = Number(url.searchParams.get('offset') ?? 0)
        return json({ items: documents.slice(offset, offset + 1), total: documents.length, limit: 1, offset })
      }
      if (url.pathname === docsPath + '/' + fixtureIds.routes && method === 'GET') {
        return json({ ...documents[1], content: '[Bad link](javascript:alert(1))\n\n<script>alert(1)</script>' })
      }
    })
    const { user } = openConsole(core + '/documents')
    expect(await screen.findByText('1–1 of 3')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Next' }))
    expect(await screen.findByText('2–2 of 3')).toBeVisible()
    await user.click(screen.getByRole('link', { name: 'Search route contract' }))
    const preview = await screen.findByRole('region', { name: 'Markdown preview' })
    expect(within(preview).getByText('Bad link')).not.toHaveAttribute('href')
    expect(preview.querySelector('script')).toBeNull()
    expect(requests(fetch, docsPath).map(({ url }) => url.searchParams.get('offset'))).toEqual(['0', '1'])
  })
})
