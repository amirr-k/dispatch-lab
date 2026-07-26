import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { ComparePage } from './ComparePage.tsx'
import { ReplayPage } from './ReplayPage.tsx'
import { stripBase } from './basePath.ts'

// still no router dependency: three routes, two of them static, is not worth
// one. Revisit if the architecture page needs nested routes.
function route() {
  const path = stripBase(window.location.pathname)
  if (path === '/compare') return <ComparePage />

  const replay = path.match(/^\/replay\/([\w-]+)$/)
  if (replay) return <ReplayPage simulationId={replay[1]} />

  return <App />
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    {route()}
  </StrictMode>,
)
