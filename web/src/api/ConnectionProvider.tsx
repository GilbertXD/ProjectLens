import { createContext, useContext, useState, useSyncExternalStore } from 'react'
import type { ReactNode } from 'react'
import { createConnectionStore } from './connection'

const ConnectionContext = createContext<ReturnType<typeof createConnectionStore> | null>(null)

export function ConnectionProvider({ children }: { children: ReactNode }) {
  const [store] = useState(() => {
    let storage: Storage | undefined
    try { storage = window.localStorage } catch { /* Storage is optional. */ }
    return createConnectionStore(storage)
  })
  return <ConnectionContext.Provider value={store}>{children}</ConnectionContext.Provider>
}

export function useConnection() {
  const store = useContext(ConnectionContext)
  if (!store) throw new Error('ConnectionProvider is required')
  return { ...useSyncExternalStore(store.subscribe, store.getSnapshot), setBaseUrl: store.setBaseUrl, setToken: store.setToken }
}
