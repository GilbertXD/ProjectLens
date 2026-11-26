import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter } from 'react-router-dom'
import { App } from '@/App'
import { routes } from '@/routes/router'
import '@/styles/global.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App router={createBrowserRouter(routes)} />
  </StrictMode>,
)
