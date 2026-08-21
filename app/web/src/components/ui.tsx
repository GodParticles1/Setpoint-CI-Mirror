import {
  AlertTriangle,
  Ban,
  CheckCircle2,
  CircleAlert,
  Eye,
  LoaderCircle,
  RefreshCw,
  type LucideIcon,
} from 'lucide-react'
import type { ButtonHTMLAttributes, ReactNode } from 'react'
import type { ItemStatus, OperationState, RunPhase } from '../api/types'
import { itemLabels, operationStateLabels, phaseLabels } from '../lib/format'

export function Button({ className = '', children, ...props }: ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button className={`button ${className}`} {...props}>{children}</button>
}

export function IconButton({ label, children, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { label: string; children: ReactNode }) {
  return <button className="icon-button" title={label} aria-label={label} {...props}>{children}</button>
}

export function PageHeader({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  return (
    <header className="page-header">
      <div><h1>{title}</h1>{description && <p>{description}</p>}</div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  )
}

const statusIcons: Record<ItemStatus, LucideIcon> = {
  safe: CheckCircle2,
  unsafe: AlertTriangle,
  manual_review: Eye,
  error: CircleAlert,
  not_applicable: Ban,
}

export function StatusBadge({ status }: { status: ItemStatus }) {
  const Icon = statusIcons[status]
  return <span className={`status status-${status}`}><Icon size={14} />{itemLabels[status]}</span>
}

export function PhaseBadge({ phase }: { phase: RunPhase }) {
  return <span className={`phase phase-${phase}`}>{phase === 'running' && <LoaderCircle size={14} className="spin" />}{phaseLabels[phase]}</span>
}

export function OperationStateBadge({ state }: { state: OperationState }) {
  return <span className={`operation-state operation-state-${state}`}>{['discovering', 'prechecking', 'running', 'verifying', 'rolling_back'].includes(state) && <LoaderCircle size={14} className="spin" />}{operationStateLabels[state]}</span>
}

export function Loading({ label = '正在读取' }: { label?: string }) {
  return <div className="state-block" role="status" aria-live="polite" aria-busy="true"><LoaderCircle className="spin" size={20} /><span>{label}</span></div>
}

export function ErrorState({ message, retry, retryLabel = '重试' }: { message: string; retry?: () => void; retryLabel?: string }) {
  return <div className="state-block state-error" role="alert"><CircleAlert size={20} /><span>{message}</span>{retry && <Button onClick={retry}><RefreshCw size={15} />{retryLabel}</Button>}</div>
}

export function EmptyState({ children }: { children: ReactNode }) {
  return <div className="state-block state-empty">{children}</div>
}
