import { Activity, Bot, LoaderCircle, Wrench } from 'lucide-react'

type ExecutionActivityProps = {
  title: string
  status: string
  active: boolean
  completed?: number
  total?: number
  compact?: boolean
}

export function ExecutionActivity({ title, status, active, completed, total, compact = false }: ExecutionActivityProps) {
  const hasTaskProgress = typeof completed === 'number' && typeof total === 'number'
  const hasProgressBar = hasTaskProgress && total > 0
  const boundedCompleted = hasProgressBar ? Math.min(Math.max(completed, 0), total) : 0
  const progress = hasProgressBar ? (boundedCompleted / total) * 100 : 0

  if (compact) {
    return <span className={`execution-activity execution-activity-compact ${active ? 'is-active' : 'is-static'}`} data-active={active ? 'true' : 'false'} role="status">
      <span className="execution-activity-compact-icon" aria-hidden="true"><Wrench size={14} /></span>
      <span>{status}</span>
    </span>
  }

  return <section className={`execution-activity execution-activity-card ${active ? 'is-active' : 'is-static'}`} data-active={active ? 'true' : 'false'} aria-live="polite" aria-label={title}>
    <div className="execution-activity-visual" aria-hidden="true">
      <Bot className="execution-activity-bot" size={24} />
      <Activity className="execution-activity-signal" size={16} />
      <LoaderCircle className="execution-activity-loader" size={16} />
    </div>
    <div className="execution-activity-copy">
      <strong>{title}</strong>
      <span>{status}</span>
      {hasTaskProgress && <small>真实任务进度：{completed} / {total}</small>}
      {hasProgressBar && <div className="execution-activity-progress" role="progressbar" aria-label="真实任务进度" aria-valuemin={0} aria-valuemax={total} aria-valuenow={boundedCompleted}>
        <span style={{ width: `${progress}%` }} />
      </div>}
    </div>
  </section>
}
