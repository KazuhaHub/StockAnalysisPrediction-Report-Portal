import React from 'react'
import ReactDOM from 'react-dom/client'
import { createBrowserRouter, RouterProvider } from 'react-router'
import App from './App'
import './i18n'
import './index.css'

// After a new deploy the hashed chunk filenames change. A long-open tab, a stale
// service-worker shell, or a brief proxy/upstream mismatch during rollout can make a
// lazy import() request a chunk that is no longer served — which would otherwise leave
// a blank page (a route chunk failed, nothing rendered). Vite fires `vite:preloadError`
// when a dynamic import fails; reload to fetch the fresh index.html (served no-cache) and
// the current chunks. Throttle via sessionStorage so a genuinely persistent failure
// (e.g. offline) reloads once instead of looping.
window.addEventListener('vite:preloadError', () => {
  const KEY = 'rp-chunk-reload-at'
  const last = Number(sessionStorage.getItem(KEY) || '0')
  if (Date.now() - last < 10_000) return
  try {
    sessionStorage.setItem(KEY, String(Date.now()))
  } catch {
    /* storage disabled — still attempt the reload below */
  }
  window.location.reload()
})

// Keep the existing route tree/providers under one stable root while enabling
// native router blockers for app draft guards (links, programmatic and POP).
const router = createBrowserRouter([{ path: '*', element: <App /> }])
ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <RouterProvider router={router} />
  </React.StrictMode>,
)
