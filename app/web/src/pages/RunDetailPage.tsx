import { ArrowLeft, Ban, CircleAlert, RefreshCw, Server } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { api, APIError } from '../api/client'
import type { CancelReport, CheckRun, ItemStatus } from '../api/types'
import { Button, ErrorState, IconButton, Loading, PageHeader, PhaseBadge, StatusBadge } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { dateTime, isTerminal, shortID } from '../lib/format'
import { ResultCounts } from './DashboardPage'

const filters: Array<{ value: 'all' | ItemStatus; label: string }> = [
  { value: 'all', label: '全部' }, { value: 'unsafe', label: '不安全' },
  { value: 'manual_review', label: '人工复核' }, { value: 'error', label: '错误' },
  { value: 'safe', label: '安全' }, { value: 'not_applicable', label: '不适用' },
]
export function pollDelay(failureCount: number) {
  return Math.min(5_000 * (2 ** Math.min(Math.max(failureCount, 0), 3)), 30_000)
}

export function RunDetailPage({ id, navigate }: { id: string; navigate: (path: string) => void }) {
  const resource = useResource((signal) => api.run(id, signal), [id])
  const nodeResource = useResource((signal) => api.nodes(signal))
  const [filter, setFilter] = useState<'all' | ItemStatus>('all')
  const [canceling, setCanceling] = useState(false)
  const [actionError, setActionError] = useState('')
  const [cancelReport, setCancelReport] = useState<CancelReport>()

  useEffect(() => {
    if (!resource.data || resource.loading || isTerminal(resource.data.status.phase)) return
    const timer = window.setTimeout(resource.refresh, pollDelay(resource.failureCount))
    return () => window.clearTimeout(timer)
  }, [resource.data?.status.phase, resource.data?.status.updated_at, resource.loading, resource.failureCount, resource.settledRevision, resource.refresh])

  const rows = useMemo(() => (resource.data?.tasks ?? []).flatMap((task) => (task.result?.items ?? []).map((item) => ({ task, item }))), [resource.data])
  const visible = filter === 'all' ? rows : rows.filter(({ item }) => item.status === filter)
  const nodeNames = useMemo(() => new Map((nodeResource.data?.nodes ?? []).map((node) => [node.id, node.hostname])), [nodeResource.data])

  if (resource.loading && !resource.data) return <Loading label="正在读取检查结果" />
  if (resource.error && !resource.data) return <ErrorState message={resource.error} retry={resource.refresh} />
  const run = resource.data!
  const attention = resultAttention(run.status.phase, run.status.counts)

  const cancel = async () => {
    setCanceling(true); setActionError(''); setCancelReport(undefined)
    try {
      const response = await api.cancelRun(id)
      resource.setData(response.run)
      setCancelReport(response.cancel_report)
    }
    catch (error) { setActionError(error instanceof APIError ? error.message : '取消失败') }
    finally { setCanceling(false) }
  }

  return <>
    <PageHeader title={run.metadata.name || '检查结果'} description={`批次 ${shortID(run.metadata.id)} · ${dateTime(run.metadata.created_at)}`} actions={<><IconButton label="返回批次" onClick={() => navigate('/runs')}><ArrowLeft size={18} /></IconButton><IconButton label="刷新" onClick={resource.refresh}><RefreshCw size={17} /></IconButton>{!isTerminal(run.status.phase) && <Button className="button-danger" disabled={canceling} onClick={cancel}><Ban size={15} />{canceling ? '正在取消' : '取消批次'}</Button>}</>} />
    {actionError && <div className="notice-error">{actionError}</div>}
    {cancelReport && <div className={cancelReport.failed_tasks > 0 ? 'notice-error' : 'notice-info'} role="status">{cancelReport.failed_tasks > 0 ? '批次已部分处理' : '取消请求已处理'}：直接取消 {cancelReport.canceled_tasks}，执行中请求取消 {cancelReport.cancel_requested_tasks}，已结束 {cancelReport.already_terminal_tasks}，失败 {cancelReport.failed_tasks}。</div>}
    {resource.error && <div className="notice-error" role="status">自动刷新失败：{resource.error}。将在 {pollDelay(resource.failureCount) / 1000} 秒后重试。</div>}
    <section className={`result-attention result-attention-${attention.tone}`} aria-label="检查批次结论"><CircleAlert size={18} /><div><strong>{attention.title}</strong><p>{attention.detail}</p></div></section>
    <section className="run-summary"><div><span>状态</span><PhaseBadge phase={run.status.phase} /></div><div><span>节点</span><strong>{run.spec.node_ids.length}</strong></div><div><span>检查项</span><strong>{run.spec.check_ids.length}</strong></div><div><span>任务</span><strong>{run.status.counts.completed_tasks} / {run.status.counts.total_tasks}</strong></div><div className="run-results"><span>检查结论</span><ResultCounts summary={run.status.counts} /></div></section>
    {nodeResource.error && <div className="notice-info">节点名称暂未加载，结果仍使用节点 ID 展示。</div>}
    <div className="segmented" role="group" aria-label="结果筛选">{filters.map((current) => <button key={current.value} className={filter === current.value ? 'active' : ''} onClick={() => setFilter(current.value)}>{current.label}</button>)}</div>
    <section className="section-block result-section">
      {rows.length === 0 ? <div className="state-block state-empty">{isTerminal(run.status.phase) ? '没有可显示的检查结果' : 'Agent 正在等待或执行任务'}</div> : visible.length === 0 ? <div className="state-block state-empty">当前筛选没有结果</div> : <div className="table-wrap result-table"><table><thead><tr><th>结论</th><th>节点</th><th>检查项</th><th>当前 / 建议</th><th>判断依据与下一步</th></tr></thead><tbody>
        {visible.map(({ task, item }) => <tr key={`${task.metadata.id}:${item.id}`} className={`result-row result-row-${item.status}`}>
          <td><StatusBadge status={item.status} /></td>
          <td><div className="result-node"><Server size={15} /><div><strong>{nodeNames.get(task.spec.node_id) || task.spec.node_id}</strong>{nodeNames.has(task.spec.node_id) && <small title={task.spec.node_id}>节点 ID {shortID(task.spec.node_id)}</small>}</div></div></td>
          <td><strong>{item.name}</strong><small title={item.id}>{item.id}</small><small className="technical-secondary">插件 {task.spec.plugin_id}</small></td>
          <td><div className="value-compare"><span><b>当前</b>{item.current_value || '无'}</span><span><b>建议</b>{item.recommended_value}</span></div></td>
          <td><div className="result-reason"><span className="result-evidence-label">检查证据</span><p>{item.evidence_summary || '没有可显示的证据摘要'}</p>
            {item.status === 'manual_review' && <div className="result-guidance guidance-review"><strong>为什么需要人工复核</strong><p>{item.review_reason || '现有证据不足以自动得出结论。'}</p></div>}
            {item.status === 'unsafe' && <div className="result-guidance guidance-unsafe"><strong>建议处理</strong><p>{item.remediation || '请结合当前策略确认处理方式。'}</p></div>}
            {item.status === 'error' && <div className="result-guidance guidance-error"><strong>检查未完成</strong><p>{item.error?.message || '检查执行未得到有效结果。'}</p></div>}
            {item.error && <details><summary>技术详情</summary><code>{item.error.code}: {item.error.message}</code></details>}
          </div></td>
        </tr>)}
      </tbody></table></div>}
    </section>
  </>
}

function resultAttention(phase: CheckRun['status']['phase'], counts: { unsafe: number; manual_review: number; error: number; not_applicable: number }) {
  if (!isTerminal(phase)) return { tone: 'running', title: '检查仍在执行', detail: '结果会随 Agent 完成任务持续更新。' }
  if (counts.error > 0) return { tone: 'error', title: `${counts.error} 个检查没有得到有效结果`, detail: '先查看“检查未完成”的原因；这些结果不能作为安全结论。' }
  if (counts.unsafe > 0) return { tone: 'unsafe', title: `${counts.unsafe} 个不安全项需要关注`, detail: '按节点查看当前值、建议值和处理建议。' }
  if (counts.manual_review > 0) return { tone: 'review', title: `${counts.manual_review} 个结果需要人工复核`, detail: '这些结果没有被计为安全项，请查看具体复核原因。' }
  return { tone: 'clear', title: '本批次没有发现需要优先处理的异常', detail: counts.not_applicable > 0 ? `${counts.not_applicable} 个检查不适用于当前环境。` : '所有已完成检查均已有明确结论。' }
}
