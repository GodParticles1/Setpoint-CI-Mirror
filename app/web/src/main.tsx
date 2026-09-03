import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import './styles.css'
import './polish.css'
import './polish-operations.css'
import './node-bootstrap.css'
import './final-ux.css'
import './xrocket-readdress.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
