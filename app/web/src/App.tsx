import {
  ClipboardCheck,
  Download,
  LayoutDashboard,
  Menu,
  Server,
  Settings,
  ShieldCheck,
  Wrench,
  X,
} from 'lucide-react'
import { useEffect, useState } from 'react'
import { ErrorState, IconButton } from './components/ui'
import { ChecksPage } from './pages/ChecksPage'
import { DashboardPage } from './pages/DashboardPage'
import { DeployPage } from './pages/DeployPage'
import { NodesPage } from './pages/NodesPage'
import { OperationRunDetailPage } from './pages/OperationRunDetailPage'
import { OperationRunsPage } from './pages/OperationRunsPage'
import { OperationsPage } from './pages/OperationsPage'
import { RunDetailPage } from './pages/RunDetailPage'
import { RunsPage } from './pages/RunsPage'
import { SettingsPage } from './pages/SettingsPage'

type Navigate = (path: string) => void

const navigation = [
  { path: '/', label: '概览', icon: LayoutDashboard },
  { path: '/nodes', label: '站点与节点', icon: Server },
  { path: '/checks', label: '检查项', icon: ShieldCheck },
  { path: '/runs', label: '检查批次', icon: ClipboardCheck },
  { path: '/operations', label: '受控操作', icon: Wrench },
  { path: '/deploy', label: 'Agent 部署', icon: Download },
  { path: '/settings', label: '设置', icon: Settings },
]

function currentPath() {
  return window.location.pathname.replace(/\/+$/, '') || '/'
}

function navigationLabel(path: string) {
  return navigation.find(({ path: target }) => target === '/' ? path === '/' : path === target || path.startsWith(`${target}/`))?.label || '概览'
}

export function decodeRouteSegment(value: string) {
  try {
    return decodeURIComponent(value)
  } catch {
    return null
  }
}

function BrandMark({ className }: { className: string }) {
  return <img className={className} src="/setpoint-mark.svg" alt="" aria-hidden="true" />
}

export function App() {
  const [path, setPath] = useState(currentPath)
  const [menuOpen, setMenuOpen] = useState(false)

  useEffect(() => {
    const onPopState = () => {
      setPath(currentPath())
      setMenuOpen(false)
    }
    window.addEventListener('popstate', onPopState)
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  const navigate: Navigate = (next) => {
    if (currentPath() !== next) window.history.pushState({}, '', next)
    setPath(next)
    setMenuOpen(false)
    window.scrollTo({ top: 0, behavior: 'instant' })
  }

  return (
    <div className="app-shell">
      <aside className={`sidebar ${menuOpen ? 'sidebar-open' : ''}`}>
        <div className="brand"><BrandMark className="brand-mark" /><strong>Setpoint</strong></div>
        <nav aria-label="主导航">
          {navigation.map(({ path: target, label, icon: Icon }) => {
            const active = target === '/' ? path === '/' : path === target || path.startsWith(`${target}/`)
            return (
              <a key={target} href={target} className={active ? 'nav-active' : ''} aria-current={active ? 'page' : undefined} onClick={(event) => { event.preventDefault(); navigate(target) }}>
                <Icon size={18} /><span className="nav-label">{label}</span>
              </a>
            )
          })}
        </nav>
        <div className="sidebar-foot"><span className="sidebar-foot-label">安全边界</span><strong>只读检查 · 受控操作</strong></div>
      </aside>
      {menuOpen && <button className="sidebar-scrim" aria-label="关闭导航" onClick={() => setMenuOpen(false)} />}
      <div className="workspace">
        <div className="mobile-bar">
          <IconButton label={menuOpen ? '关闭导航' : '打开导航'} onClick={() => setMenuOpen((value) => !value)}>{menuOpen ? <X size={20} /> : <Menu size={20} />}</IconButton>
          <div className="mobile-brand"><BrandMark className="mobile-brand-mark" /><div><strong>Setpoint</strong><span>{navigationLabel(path)}</span></div></div>
        </div>
        <main>{renderPage(path, navigate)}</main>
      </div>
    </div>
  )
}

function renderPage(path: string, navigate: Navigate) {
  if (path === '/') return <DashboardPage navigate={navigate} />
  if (path === '/nodes') return <NodesPage navigate={navigate} />
  if (path === '/checks') return <ChecksPage navigate={navigate} />
  if (path === '/runs') return <RunsPage navigate={navigate} />
  const runMatch = path.match(/^\/runs\/([^/]+)$/)
  if (runMatch) {
    const id = decodeRouteSegment(runMatch[1])
    if (id === null) return <ErrorState message="检查批次地址无效" retry={() => navigate('/runs')} retryLabel="返回检查批次" />
    return <RunDetailPage id={id} navigate={navigate} />
  }
  if (path === '/operations') return <OperationsPage navigate={navigate} />
  if (path === '/operations/runs') return <OperationRunsPage navigate={navigate} />
  const operationRunMatch = path.match(/^\/operations\/runs\/([^/]+)$/)
  if (operationRunMatch) {
    const id = decodeRouteSegment(operationRunMatch[1])
    if (id === null) return <ErrorState message="操作记录地址无效" retry={() => navigate('/operations/runs')} retryLabel="返回操作记录" />
    return <OperationRunDetailPage id={id} navigate={navigate} />
  }
  if (path === '/deploy') return <DeployPage />
  if (path === '/settings') return <SettingsPage />
  return <DashboardPage navigate={navigate} />
}
