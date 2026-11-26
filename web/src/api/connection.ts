const storageKey = 'projectlens.apiOrigin'

export function parseApiBaseUrl(value: string): string | undefined {
  const input = value.trim()
  if (!input) return ''
  if (!/^https?:\/\/[^/?#@\\\s]+\/?$/i.test(input)) return undefined
  try {
    return new URL(input).origin
  } catch {
    return undefined
  }
}

export interface ApiConnection {
  apiBaseUrl: string
  revision: number
  getToken: () => string
  onUnauthorized: () => void
}

export function createConnectionStore(storage?: Storage) {
  let token = ''
  let revision = 0
  const listeners = new Set<() => void>()
  let baseUrl = ''
  try {
    baseUrl = parseApiBaseUrl(storage?.getItem(storageKey) ?? '') ?? ''
  } catch {
    // Storage can be disabled by browser policy.
  }

  function scope(apiBaseUrl: string): ApiConnection {
    const currentRevision = revision
    return {
      apiBaseUrl, revision: currentRevision,
      getToken() {
        if (revision !== currentRevision) throw new DOMException('Connection changed', 'AbortError')
        return token
      },
      onUnauthorized() {
        if (revision === currentRevision && !snapshot.unauthorized) {
          snapshot = { ...snapshot, unauthorized: true }
          notify()
        }
      },
    }
  }

  let snapshot = { connection: scope(baseUrl), unauthorized: false }
  function notify() {
    listeners.forEach((listener) => listener())
  }
  function update(apiBaseUrl: string) {
    revision += 1
    snapshot = { connection: scope(apiBaseUrl), unauthorized: false }
    notify()
  }
  return {
    getSnapshot: () => snapshot,
    subscribe(listener: () => void) {
      listeners.add(listener)
      return () => { listeners.delete(listener) }
    },
    setBaseUrl(value: string) {
      const origin = parseApiBaseUrl(value)
      if (origin === undefined) return false
      if (origin === snapshot.connection.apiBaseUrl) return true
      try {
        if (origin) storage?.setItem(storageKey, origin)
        else storage?.removeItem(storageKey)
      } catch {
        // Apply the connection for this tab even when storage is unavailable.
      }
      token = ''
      update(origin)
      return true
    },
    setToken(value: string) {
      const next = value.trim()
      if (next === token) return
      token = next
      update(snapshot.connection.apiBaseUrl)
    },
  }
}
