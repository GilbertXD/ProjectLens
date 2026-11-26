import { act, renderHook } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { createElement } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, apiFetch, apiFetchWithMeta } from '../client'
import { createConnectionStore, parseApiBaseUrl } from '../connection'
import { ConnectionProvider } from '../ConnectionProvider'
import { getReadiness, pingApi } from '../endpoints'
import { jobPollInterval } from '../queries'
import { useJobPoller } from '../useJobPoller'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
  localStorage.clear()
  sessionStorage.clear()
})

describe('API connection and transport', () => {
  it('sends a bearer token only on protected same-backend routes', async () => {
    const store = createConnectionStore()
    store.setToken('session-token')
    const { connection } = store.getSnapshot()
    const fetch = vi.fn(async (_url: string, _init?: RequestInit) => Response.json({}))
    vi.stubGlobal('fetch', fetch)

    const paths = ['/api/v1/ping', '/healthz', '/readyz', '/openapi.json', '/openapi.yaml']
    for (const path of paths) await apiFetch(connection, path, { headers: { Authorization: 'caller-supplied' } })
    expect(fetch.mock.calls.map(([url, init]) => [url, new Headers(init?.headers).get('Authorization')])).toEqual([
      ['/api/v1/ping', 'Bearer session-token'],
      ['/healthz', null],
      ['/readyz', null],
      ['/openapi.json', null],
      ['/openapi.yaml', null],
    ])
    expect(fetch.mock.calls[0][1]).toMatchObject({ redirect: 'error', credentials: 'omit' })
    await expect(apiFetch(connection, '//other.example/api/v1/ping')).rejects.toThrow('Expected a canonical API path')
    await expect(apiFetch(connection, '/api/v1/../../healthz')).rejects.toThrow('Expected a canonical API path')
    expect(fetch).toHaveBeenCalledTimes(paths.length)
  })

  it('normalizes origins and clears the tab token when the origin changes', async () => {
    const store = createConnectionStore(localStorage)
    const fetch = vi.fn(async (_url: string, _init?: RequestInit) => Response.json({ message: 'pong' }))
    vi.stubGlobal('fetch', fetch)
    for (const invalid of [
      'file:///tmp', 'https://user:pass@example.test', 'https://@example.test', 'https://example.test/a',
      'https://example.test/?', 'https://example.test/#', 'https://example.test/../',
    ]) expect(parseApiBaseUrl(invalid)).toBeUndefined()

    store.setToken('session-token')
    const previous = store.getSnapshot().connection
    expect(store.setBaseUrl('https://example.test/path')).toBe(false)
    expect(previous.getToken()).toBe('session-token')
    expect(store.setBaseUrl(' https://EXAMPLE.test:443/ ')).toBe(true)
    const { connection } = store.getSnapshot()
    expect(connection.apiBaseUrl).toBe('https://example.test')
    await pingApi(connection)
    expect(fetch.mock.calls[0][0]).toBe('https://example.test/api/v1/ping')
    expect(new Headers(fetch.mock.calls[0][1]?.headers).has('Authorization')).toBe(false)
    await expect(pingApi(previous)).rejects.toMatchObject({ name: 'AbortError' })
    previous.onUnauthorized()
    expect(store.getSnapshot().unauthorized).toBe(false)
    store.setToken('new-token')
    expect(createConnectionStore(localStorage).getSnapshot().connection.getToken()).toBe('')
    expect(Object.entries(localStorage)).toEqual([['projectlens.apiOrigin', 'https://example.test']])
    expect(sessionStorage.length).toBe(0)
    store.setBaseUrl('')
    expect(localStorage.length).toBe(0)
  })

  it('reports an invalid header without exposing the token', async () => {
    const store = createConnectionStore()
    store.setToken('invalid\nsecret')
    const fetch = vi.fn()
    vi.stubGlobal('fetch', fetch)
    const error = await pingApi(store.getSnapshot().connection).catch((failure: unknown) => failure)
    expect(error).toMatchObject({ code: 'invalid_token', message: 'Enter a valid Bearer token in connection settings.' })
    expect(fetch).not.toHaveBeenCalled()
  })

  it('handles 204 without parsing and exposes response metadata', async () => {
    const { connection } = createConnectionStore().getSnapshot()
    const response = new Response(null, { status: 204, headers: { 'X-Request-ID': 'req-empty' } })
    const parse = vi.spyOn(response, 'json')
    const fetch = vi.fn().mockResolvedValueOnce(response).mockResolvedValueOnce(
      Response.json({ items: [] }, { headers: { ETag: '"4"', 'X-Search-Cache': 'hit', 'X-Search-Version': '17' } }),
    )
    vi.stubGlobal('fetch', fetch)
    await expect(apiFetch<void>(connection, '/api/v1/projects/123', { method: 'DELETE' })).resolves.toBeUndefined()
    expect(parse).not.toHaveBeenCalled()
    await expect(apiFetchWithMeta(connection, '/api/v1/projects/123/search?q=sample')).resolves.toMatchObject({
      data: { items: [] }, status: 200, etag: '"4"', searchCache: 'hit', searchVersion: '17',
    })
  })

  it('preserves structured errors and reports authenticated 401s', async () => {
    const store = createConnectionStore()
    const onUnauthorized = vi.fn()
    const connection = { ...store.getSnapshot().connection, onUnauthorized }
    const fetch = vi.fn()
      .mockResolvedValueOnce(Response.json({ error: 'slug already exists', code: 'conflict', request_id: 'body-id' },
        { status: 409, headers: { 'X-Request-ID': 'header-id' } }))
      .mockResolvedValueOnce(Response.json({ error: 'invalid bearer token', code: 'unauthorized', request_id: 'body-id' }, { status: 401 }))
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
    vi.stubGlobal('fetch', fetch)
    const conflict = await apiFetch(connection, '/api/v1/projects').catch((error: unknown) => error)
    expect(conflict).toBeInstanceOf(ApiError)
    expect(conflict).toMatchObject({ status: 409, message: 'slug already exists', code: 'conflict', requestId: 'header-id' })
    await expect(pingApi(connection)).rejects.toMatchObject({ status: 401, code: 'unauthorized', requestId: 'body-id' })
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    await expect(apiFetch(connection, '/healthz')).rejects.toMatchObject({ status: 401 })
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('accepts a valid 503 readiness payload and rejects other server errors', async () => {
    const { connection } = createConnectionStore().getSnapshot()
    const degraded = { status: 'unavailable', checks: { postgres: 'unavailable', redis: 'degraded' } }
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(Response.json(degraded, { status: 503 }))
      .mockResolvedValueOnce(Response.json({ error: 'shutdown', code: 'service_unavailable' }, { status: 503 })))
    await expect(getReadiness(connection)).resolves.toEqual(degraded)
    await expect(getReadiness(connection)).rejects.toMatchObject({ status: 503, code: 'service_unavailable' })
  })
})

describe('job polling', () => {
  it.each(['succeeded', 'failed', 'cancelled'])('stops polling after %s and calls onSettled once', async (terminal) => {
    vi.useFakeTimers()
    const fetch = vi.fn()
      .mockImplementationOnce(async () => Response.json({ id: 'sync-id', status: 'pending' }))
      .mockImplementationOnce(async () => Response.json({ id: 'sync-id', status: 'running' }))
      .mockImplementation(async () => Response.json({ id: 'sync-id', status: terminal }))
    vi.stubGlobal('fetch', fetch)
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } })
    const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client: queryClient },
      createElement(ConnectionProvider, { children }))
    const onSettled = vi.fn()
    const { result, unmount } = renderHook(() => useJobPoller('sync-id', onSettled), { wrapper })
    const tick = async (ms: number) => {
      await act(async () => { await vi.advanceTimersByTimeAsync(ms) })
    }

    await tick(5)
    expect(result.current.data?.status).toBe('pending')
    await tick(jobPollInterval)
    expect(result.current.data?.status).toBe('running')
    expect(onSettled).not.toHaveBeenCalled()
    await tick(jobPollInterval)
    expect(result.current.data?.status).toBe(terminal)
    expect(onSettled).toHaveBeenCalledExactlyOnceWith({ id: 'sync-id', status: terminal })
    await tick(jobPollInterval * 3)
    expect(fetch).toHaveBeenCalledTimes(3)
    expect(fetch.mock.calls.every(([url]) => url === '/api/v1/jobs/sync-id')).toBe(true)
    unmount()
    queryClient.clear()
  })
})
