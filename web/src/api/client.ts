import type { ApiConnection } from './connection'

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly requestId?: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

interface ApiOptions extends RequestInit {
  acceptError?: (status: number, body: unknown) => boolean
}

function errorBody(body: unknown) {
  if (!body || typeof body !== 'object') return {}
  const { error, code, request_id } = body as Record<string, unknown>
  return {
    message: typeof error === 'string' ? error : undefined,
    code: typeof code === 'string' ? code : undefined,
    requestId: typeof request_id === 'string' ? request_id : undefined,
  }
}

export async function apiFetchWithMeta<T>(connection: ApiConnection, path: string, options: ApiOptions = {}) {
  const relativeBase = 'https://relative.invalid'
  const url = new URL(path, relativeBase)
  if (!path.startsWith('/') || url.origin !== relativeBase || url.pathname + url.search !== path) {
    throw new Error('Expected a canonical API path')
  }
  const { acceptError, ...init } = options
  const headers = new Headers(init.headers)
  headers.delete('Authorization')
  const authenticated = url.pathname.startsWith('/api/v1/')
  const token = authenticated ? connection.getToken() : ''
  if (token) {
    try {
      headers.set('Authorization', 'Bearer ' + token)
    } catch {
      throw new ApiError(0, 'invalid_token', 'Enter a valid Bearer token in connection settings.')
    }
  }
  headers.set('Accept', 'application/json')

  let response: Response
  try {
    response = await fetch(connection.apiBaseUrl + path, { ...init, headers, credentials: 'omit', redirect: 'error' })
  } catch (error) {
    if (init.signal?.aborted || (error instanceof Error && error.name === 'AbortError')) throw error
    throw new ApiError(0, 'network_error', 'Could not reach the ProjectLens API. Check the API base URL and network connection.')
  }

  const meta = {
    status: response.status,
    etag: response.headers.get('ETag') ?? undefined,
    requestId: response.headers.get('X-Request-ID') ?? undefined,
    searchCache: response.headers.get('X-Search-Cache') ?? undefined,
    searchVersion: response.headers.get('X-Search-Version') ?? undefined,
  }
  if (authenticated && response.status === 401) connection.onUnauthorized()

  let body: unknown
  if (response.status !== 204) {
    try {
      body = await response.json()
    } catch (error) {
      if (init.signal?.aborted) throw error
      if (response.ok) throw new ApiError(response.status, 'invalid_response', 'The API returned an invalid JSON response.', meta.requestId)
    }
  }
  if (!response.ok && !acceptError?.(response.status, body)) {
    const error = errorBody(body)
    throw new ApiError(response.status, error.code ?? 'http_error',
      error.message ?? (response.statusText || 'API request failed (' + response.status + ').'),
      meta.requestId ?? error.requestId)
  }
  return { data: body as T, ...meta }
}

export async function apiFetch<T>(connection: ApiConnection, path: string, options?: ApiOptions): Promise<T> {
  return (await apiFetchWithMeta<T>(connection, path, options)).data
}
