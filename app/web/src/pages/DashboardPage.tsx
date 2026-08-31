import { ArrowRight, CircleAlert, ClipboardCheck, Eye, MonitorCheck, Play, RefreshCw, ServerOff, Wrench } from 'lucide-react'
import { api } from '../api/client'
import type { DashboardSummary } from '../api/types'
import { Button, EmptyState, ErrorState, IconButton, Loading, PageHeader, PhaseBadge } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { dateTime, shortID } from '../lib/format'

export function DashboardPage({ navigate }: { navigate: (path: string) => void }) {
  const resource = useResource(async (signal) => {
    const [summary, runs] = await Promise.all([api.dashboard(signal), api.runs(0, signal)])
    return { summary, runs: runs.runs.slice(0, 6) }
  })

  if (resource.loading && !resource.data) return <Loading label="正在汇总运行状态" />
  if (resource.error && !resource.data) return <ErrorState message={resource.error} retry={resource.refresh} />
  const { summary, runs } = resource.data!
  const attention = attentionSummary(summary)

  return (
    <>
      <PageHeader title="运行概览" description={`最近检查：${dateTime(summary.last_check_at)}`} actions={<><IconButton label="刷新" onClick={resource.refresh}><RefreshCw size={17} /></IconButton><Button className="button-primary" onClick={() => navigate('/runs')}><Play size={16} />发起检查</Button></>} />
      <section className={`attention-panel attention-${attention.tone}`} aria-label="当前关注事项">
        <div className="attention-icon">{attention.tone === 'clear' ? <MonitorCheck size={20} /> : <CircleAlert size={20} />}</div>
        <div><span>当前关注</span><strong>{attention.title}</strong><p>{attention.detail}</p></div>
        <Button className="button-quiet" onClick={() => navigate(attention.path)}>{attention.action}<ArrowRight size={15} /></Button>
      </section>
      <section className="metric-grid" aria-label="节点和检查统计">
        <Metric icon={MonitorCheck} label="在线节点" value={summary.nodes_online} detail={`共 ${summary.nodes_total} 个登记节点`} tone="green" />
        <Metric icon={ServerOff} label="未在线 Agent" value={summary.nodes_offline} detail="表示 Agent 当前不可达或未在线" tone={summary.nodes_offline ? 'red' : 'neutral'} />
        <Metric icon={CircleAlert} label="不安全项" value={summary.unsafe} detail="需要确认配置差异" tone={summary.unsafe ? 'amber' : 'neutral'} priority={summary.unsafe > 0} />
        <Metric icon={Eye} label="人工复核" value={summary.manual_review} detail="不计入安全项" tone={summary.manual_review ? 'manual' : 'neutral'} />
        <Metric icon={CircleAlert} label="检查错误" value={summary.error} detail="检查未得到有效结果" tone={summary.error ? 'red' : 'neutral'} priority={summary.error > 0} />
      </section>
      <section className="work-area-grid" aria-label="主要工作入口">
        <article className="work-area-card">
          <span className="work-area-icon"><ClipboardCheck size={19} /></span>
          <div><span>Checks</span><h2>只读安全检查</h2><p>选择节点、检查项、集合或策略，创建只读检查批次并查看结果。</p></div>
          <div className="work-area-actions"><Button onClick={() => navigate('/checks')}>浏览检查项</Button><Button className="button-primary" onClick={() => navigate('/runs')}>发起检查<ArrowRight size={15} /></Button></div>
        </article>
        <article className="work-area-card work-area-planning">
          <span className="work-area-icon"><Wrench size={19} /></span>
          <div><span>Controlled Operations</span><h2>受控操作</h2><p>发现、前置检查、计划、受控执行、验证与安全恢复；具体可用能力由所选 Operation 的 Server 状态决定。</p></div>
          <div className="work-area-actions"><Button onClick={() => navigate('/operations')}>查看受控操作<ArrowRight size={15} /></Button></div>
        </article>
      </section>
      <section className="section-block">
        <div className="section-heading"><div><h2>最近检查批次</h2><p>优先查看异常、人工复核和未完成批次</p></div><Button className="button-quiet" onClick={() => navigate('/runs')}>查看全部<ArrowRight size={15} /></Button></div>
        {runs.length === 0 ? <EmptyState>尚未创建检查批次</EmptyState> : <div className="table-wrap"><table><thead><tr><th>批次</th><th>状态</th><th>节点 / 检查</th><th>结果</th><th>创建时间</th><th /></tr></thead><tbody>{runs.map((run) => <tr key={run.metadata.id}><td><strong>{run.metadata.name || '未命名检查'}</strong><small title={run.metadata.id}>{shortID(run.metadata.id)}</small></td><td><PhaseBadge phase={run.status.phase} /></td><td>{run.spec.node_ids.length} / {run.spec.check_ids.length}</td><td><ResultCounts summary={run.status.counts} /></td><td>{dateTime(run.metadata.created_at)}</td><td><IconButton label={`查看批次 ${run.metadata.name || shortID(run.metadata.id)}`} onClick={() => navigate(`/runs/${encodeURIComponent(run.metadata.id)}`)}><ArrowRight size={17} /></IconButton></td></tr>)}</tbody></table></div>}
      </section>
    </>
  )
}

function Metric({ icon: Icon, label, value, detail, tone, priority = false }: { icon: typeof MonitorCheck; label: string; value: number; detail: string; tone: string; priority?: boolean }) {
  return <div className={`metric metric-${tone} ${priority ? 'metric-priority' : ''}`}><div className="metric-icon"><Icon size={19} /></div><div><span>{label}</span><strong>{value}</strong><small>{detail}</small></div></div>
}

function attentionSummary(summary: DashboardSummary) {
  if (summary.error > 0) return { tone: 'critical', title: `${summary.error} 个检查错误需要处理`, detail: '检查未得到有效结论，先查看失败原因，再判断是否需要重试或人工处理。', action: '查看检查批次', path: '/runs' }
  if (summary.nodes_offline > 0) return { tone: 'critical', title: `${summary.nodes_offline} 个 Agent 当前未在线`, detail: '这些 Agent 当前不可达或未在线，无法接收新的检查任务；这不等同于服务器故障。', action: '查看节点', path: '/nodes' }
  if (summary.unsafe > 0) return { tone: 'warning', title: `${summary.unsafe} 个不安全项待确认`, detail: '这些结果来自只读检查，可进入最近批次查看具体节点、检查项与证据。', action: '查看检查批次', path: '/runs' }
  if (summary.manual_review > 0) return { tone: 'review', title: `${summary.manual_review} 个结果需要人工复核`, detail: '证据不足或策略需要人工判断的结果不会被当作安全项。', action: '查看检查批次', path: '/runs' }
  return { tone: 'clear', title: '当前没有需要优先处理的检查异常', detail: `在线节点 ${summary.nodes_online} 个；仍可从检查批次继续发起只读检查。`, action: '发起检查', path: '/runs' }
}

export function ResultCounts({ summary }: { summary: DashboardSummary | { safe: number; unsafe: number; manual_review: number; error: number; not_applicable: number } }) {
  const label = `安全 ${summary.safe}，不安全 ${summary.unsafe}，人工复核 ${summary.manual_review}，检查错误 ${summary.error}，不适用 ${summary.not_applicable}`
  return <div className="result-counts" aria-label={label}><span className="count-safe" title="安全"><i aria-hidden="true">安</i>{summary.safe}</span><span className="count-unsafe" title="不安全"><i aria-hidden="true">险</i>{summary.unsafe}</span><span className="count-review" title="人工复核"><i aria-hidden="true">审</i>{summary.manual_review}</span><span className="count-error" title="检查错误"><i aria-hidden="true">错</i>{summary.error}</span><span className="count-na" title="不适用"><i aria-hidden="true">免</i>{summary.not_applicable}</span></div>
}
