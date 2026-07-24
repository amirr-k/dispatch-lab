import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { ComparePage } from './ComparePage.tsx'

// no router dependency yet - just enough to serve the one extra route this
// phase adds. Revisit once more routes (replay, architecture) land.
const page = window.location.pathname === '/compare' ? <ComparePage /> : <App />

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    {page}
  </StrictMode>,
)
