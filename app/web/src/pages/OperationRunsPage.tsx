import { ArrowLeft, ArrowRight, RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'
import { api } from '../api/client'
import { Button, EmptyState, ErrorState, IconButton, Loading, OperationStateBadge, PageHeader } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { dateTime, operationTerminal, shortID } from '../lib/format'

export function operationPollDelay(failureCount: number) {
  return Math.min(5_000 * (2 ** Math.min(Math.max(failureCount, 0), 3)), 30_000)
}

export function OperationRunsPage({ navigate }: { navigate: (path: string) => void }) {
  const [offset, setOffset] = useState(0)
  const resource = useResource((signal) => api.operationRuns(offset, signal), [offset])
  useEffect(() => {
    if (!resource.data?.runs.some((run) => !operationTerminal(run.status.state)) || resource.loading) return
    const timer = window.setTimeout(resource.refresh, operationPollDelay(resource.failureCount))
    return () => window.clearTimeout(timer)
  }, [resource.data?.runs, resource.loading, resource.failureCount, resource.settledRevision, resource.refresh])

  if (resource.loading && !resource.data) return <Loading label="正在读取操作记录" />
  if (resource.error && !resource.data) return <ErrorState message={resource.error} retry={resource.refresh} />
  const runs = resource.data?.runs ?? []
  return <>
    <PageHeader title="操作记录" description="发现、前置检查和计划的持久化记录" actions={<IconButton label="刷新" onClick={resource.refresh}><RefreshCw size={17} /></IconButton>} />
    {resource.error && <div className="notice-error" role="status">刷新失败，将按退避策略重试：{resource.error}</div>}
    {runs.length === 0 ? <EmptyState>{offset === 0 ? '尚未创建操作计划' : '当前页没有操作记录'}</EmptyState> : <div className="table-wrap"><table><thead><tr><th>操作</th><th>状态</th><th>执行节点</th><th>检查点</th><th>创建时间</th><th /></tr></thead><tbody>{runs.map((run) => <tr key={run.metadata.id}><td><strong>{run.spec.operation_id}</strong><small>{shortID(run.metadata.id)} · v{run.spec.operation_version}</small></td><td><OperationStateBadge state={run.status.state} /></td><td>{run.spec.node_id}</td><td><code>{run.status.checkpoint}</code></td><td>{dateTime(run.metadata.created_at)}</td><td><IconButton label="查看操作" onClick={() => navigate(`/operations/runs/${encodeURIComponent(run.metadata.id)}`)}><ArrowRight size={17} /></IconButton></td></tr>)}</tbody></table></div>}
    <nav className="pagination" aria-label="操作记录分页"><Button disabled={offset === 0} onClick={() => setOffset((value) => Math.max(0, value - 50))}><ArrowLeft size={15} />上一页</Button><span>第 {Math.floor(offset / 50) + 1} 页</span><Button disabled={runs.length < 50} onClick={() => setOffset((value) => value + 50)}>下一页<ArrowRight size={15} /></Button></nav>
  </>
}
