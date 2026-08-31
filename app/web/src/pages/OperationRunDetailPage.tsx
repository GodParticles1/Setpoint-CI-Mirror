import { ArrowLeft, Ban, Check, RefreshCw, ShieldAlert } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api, APIError } from '../api/client'
import type { OperationFinding, OperationRun, OperationTarget } from '../api/types'
import { ExecutionActivity } from '../components/ExecutionActivity'
import { Button, ErrorState, IconButton, Loading, OperationStateBadge, PageHeader } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { dateTime, operationTerminal, shortID } from '../lib/format'
import { IdempotentOperation } from '../lib/idempotency'
import { operationActivityLabel, operationPresentation } from '../lib/operationPresentation'
import { operationPollDelay } from './OperationRunsPage'

const atomicExchangeUnavailable = 'ATOMIC_EXCHANGE_NOT_AVAILABLE'

export function OperationRunDetailPage({ id, navigate }: { id: string; navigate: (path: string) => void }) {
  const resource = useResource(async (signal) => {
    const run = await api.operationRun(id, signal)
    const definition = await api.operation(run.spec.operation_id, signal)
    return { run, definition }
  }, [id])
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [canceling, setCanceling] = useState(false)
  const [actionError, setActionError] = useState('')
  const [confirmationGate, setConfirmationGate] = useState('')
  const confirmOperation = useRef(new IdempotentOperation()).current

  useEffect(() => {
    const run = resource.data?.run
    if (!run || operationTerminal(run.status.state) || resource.loading) return
    const timer = window.setTimeout(resource.refresh, operationPollDelay(resource.failureCount))
    return () => window.clearTimeout(timer)
  }, [resource.data?.run.status.state, resource.data?.run.status.updated_at, resource.loading, resource.failureCount, resource.settledRevision, resource.refresh])

  if (resource.loading && !resource.data) return <Loading label="正在读取操作计划" />
  if (resource.error && !resource.data) return <ErrorState message={resource.error} retry={resource.refresh} />
  const { run, definition } = resource.data!
  const presentation = operationPresentation(definition)
  const canConfirm = run.status.state === 'awaiting_confirmation' && Boolean(run.plan_digest)
  const canCancel = !operationTerminal(run.status.state) && !['running', 'verifying', 'rolling_back'].includes(run.status.state)
  const outcome = planningOutcome(run)

  const confirm = async () => {
    if (!run.plan_digest) return
    setConfirming(true)
    setActionError('')
    const fingerprint = `${run.metadata.id}:${run.plan_digest}`
    try {
      await api.confirmOperationRun(run.metadata.id, run.plan_digest, confirmOperation.keyFor(fingerprint))
      confirmOperation.complete()
      setConfirmOpen(false)
      resource.refresh()
    } catch (error) {
      if (error instanceof APIError && error.code === 'product_apply_disabled') {
        setConfirmationGate('product_apply_disabled')
        setConfirmOpen(false)
      } else {
        setActionError(error instanceof APIError ? error.message : '确认计划失败')
      }
    } finally {
      setConfirming(false)
    }
  }

  const cancel = async () => {
    setCanceling(true)
    setActionError('')
    try {
      const canceled = await api.cancelOperationRun(run.metadata.id)
      resource.setData({ run: canceled, definition })
    } catch (error) {
      setActionError(error instanceof APIError ? error.message : '取消操作失败')
    } finally {
      setCanceling(false)
    }
  }

  return <>
    <PageHeader title={presentation.name} description={`操作 ${shortID(run.metadata.id)} · ${dateTime(run.metadata.created_at)}`} actions={<><IconButton label="返回操作记录" onClick={() => navigate('/operations/runs')}><ArrowLeft size={18} /></IconButton><IconButton label="刷新" onClick={resource.refresh}><RefreshCw size={17} /></IconButton>{canCancel && <Button className="button-danger" disabled={canceling} onClick={cancel}><Ban size={15} />{canceling ? '正在取消' : '取消操作'}</Button>}{canConfirm && <Button className="button-primary" onClick={() => setConfirmOpen(true)}><Check size={16} />确认当前计划</Button>}</>} />
    {resource.error && <div className="notice-error" role="status">自动刷新失败，将按退避策略重试：{resource.error}</div>}
    {actionError && <div className="notice-error" role="alert">{actionError}</div>}
    {!operationTerminal(run.status.state) && <ExecutionActivity title="受控操作进度" status={operationActivityLabel(run.status.state)} active />}
    <section className={`operation-outcome operation-outcome-${outcome.tone}`} aria-label="操作规划结论"><ShieldAlert size={19} /><div><span>当前结论</span><strong>{outcome.title}</strong><p>{outcome.detail}</p></div></section>
    {!definition.availability.apply && <div className="operation-gate" role="status"><ShieldAlert size={19} /><div><strong>实际变更执行尚未开放</strong><p>当前页面只用于发现、前置检查、计划、影响核对与精确计划确认；确认后也不会执行 Apply。</p><code>{confirmationGate || definition.availability.block_code}</code></div></div>}
    <section className="planning-progress" aria-label="规划阶段">
      <PlanningStep index="1" label="Discovery" description="目标发现" ready={Boolean(run.discovery)} />
      <PlanningStep index="2" label="Precheck" description="安全前置" ready={Boolean(run.precheck)} />
      <PlanningStep index="3" label="Plan" description="操作计划" ready={Boolean(run.plan)} />
      <PlanningStep index="4" label="Impact" description="影响评估" ready={Boolean(run.impact)} />
      <PlanningStep index="5" label="Confirmation" description="计划确认" ready={['confirmed', 'ready_to_apply'].includes(run.status.state)} current={run.status.state === 'awaiting_confirmation'} />
    </section>
    <section className="operation-summary"><div><span>规划状态</span><OperationStateBadge state={run.status.state} /></div><div><span>执行节点</span><strong>{run.spec.node_id}</strong></div><div><span>当前检查点</span><code>{run.status.checkpoint}</code></div><div><span>计划摘要</span><code>{shortID(run.plan_digest || '尚未生成')}</code></div></section>
    {run.status.block && <BoundaryPanel title="阻断原因" code={run.status.block.code} message={run.status.block.message} safeNext={run.status.block.safe_next_action} manualReview={run.status.block.manual_review} blocking />}
    {run.status.recovery && <BoundaryPanel title="恢复状态" code={run.status.recovery.code} message={run.status.recovery.checkpoint || '当前检查点未提供'} safeNext={run.status.recovery.safe_next_action} manualReview={run.status.recovery.manual_review} />}
    <section className="operation-detail-grid">
      <DetailSection title="目标发现" ready={Boolean(run.discovery)}>{run.discovery && <><SummaryState positive={run.discovery.applicable} positiveText="适用" negativeText="不适用" /><p>{run.discovery.summary}</p><TargetList targets={run.discovery.targets} /><Findings findings={run.discovery.findings} /></>}</DetailSection>
      <DetailSection title="前置检查" ready={Boolean(run.precheck)}>{run.precheck && <><SummaryState positive={run.precheck.passed} positiveText="通过" negativeText="未通过" /><p>{run.precheck.summary}</p><Findings findings={run.precheck.findings} /></>}</DetailSection>
      <DetailSection title="操作计划" ready={Boolean(run.plan)} wide>{run.plan && <><p>{run.plan.summary}</p><div className="table-wrap"><table><thead><tr><th>步骤</th><th>目标</th><th>动作</th><th>检查点</th><th>写入</th></tr></thead><tbody>{run.plan.steps.map((step) => <tr key={step.id}><td><strong>{step.name}</strong><small>{step.id}</small></td><td>{formatTarget(step.target)}</td><td>{step.action}</td><td><code>{step.checkpoint}</code></td><td>{step.writes ? '是' : '否'}</td></tr>)}</tbody></table></div><Findings findings={run.plan.findings} /></>}</DetailSection>
      <DetailSection title="影响评估" ready={Boolean(run.impact)} wide>{run.impact && <><div className="impact-summary"><span className={`risk risk-${run.impact.risk}`}>{riskLabel(run.impact.risk)}</span><p>{run.impact.summary}</p></div><dl className="impact-facts"><div><dt>停机</dt><dd>{run.impact.requires_downtime ? '需要' : '不需要'}</dd></div><div><dt>写入围栏</dt><dd>{run.impact.requires_write_fence ? '需要' : '不需要'}</dd></div><div><dt>预计数据变化</dt><dd>{formatBytes(run.impact.estimated_data_change_bytes)}</dd></div></dl>{run.impact.changes.length > 0 && <div className="table-wrap"><table><thead><tr><th>目标</th><th>变更前</th><th>变更后</th><th>风险</th></tr></thead><tbody>{run.impact.changes.map((change, index) => <tr key={`${formatTarget(change.target)}:${index}`}><td>{formatTarget(change.target)}</td><td>{change.before}</td><td>{change.after}</td><td>{change.risk}</td></tr>)}</tbody></table></div>}</>}</DetailSection>
      <section className="operation-detail operation-detail-wide"><header><h2>计划确认</h2><span>{canConfirm ? '待确认' : ['confirmed', 'ready_to_apply'].includes(run.status.state) ? '已确认' : '等待中'}</span></header>{canConfirm ? <p>计划和影响摘要已冻结，可确认当前 exact plan digest。确认只是安全门，不会启动实际变更。</p> : ['confirmed', 'ready_to_apply'].includes(run.status.state) ? <p>当前计划已经确认。Product Apply 仍保持关闭。</p> : <p className="muted">只有计划与影响摘要完整冻结后才可确认。</p>}</section>
    </section>
    {confirmOpen && <div className="dialog-layer" role="dialog" aria-modal="true" aria-labelledby="confirm-plan-title"><button className="dialog-scrim" aria-label="关闭确认" onClick={() => setConfirmOpen(false)} /><section className="dialog"><header><h2 id="confirm-plan-title">确认当前计划</h2></header><div className="dialog-body"><p className="confirm-copy">确认只绑定当前已保存的目标、参数、计划和影响摘要。此动作不会启动实际变更，Product Apply 仍保持关闭。</p><dl className="confirm-digest"><div><dt>操作</dt><dd>{presentation.name}</dd></div><div><dt>节点</dt><dd>{run.spec.node_id}</dd></div>{run.impact && <div><dt>影响摘要</dt><dd>{run.impact.summary}</dd></div>}</dl><details className="technical-plan-digest"><summary>技术摘要 · exact plan digest</summary><code>{run.plan_digest}</code></details><div className="dialog-actions"><Button onClick={() => setConfirmOpen(false)}>返回</Button><span /><Button className="button-primary" disabled={confirming} onClick={confirm}>{confirming ? '正在确认' : '确认当前计划'}</Button></div></div></section></div>}
  </>
}

function PlanningStep({ index, label, description, ready, current = false }: { index: string; label: string; description: string; ready: boolean; current?: boolean }) {
  const state = ready ? 'ready' : current ? 'current' : 'waiting'
  return <div className={`planning-step planning-step-${state}`}><span>{index}</span><div><strong>{label}</strong><small>{description}</small></div><i>{ready ? '已记录' : current ? '待确认' : '等待'}</i></div>
}

function DetailSection({ title, ready, wide = false, children }: { title: string; ready: boolean; wide?: boolean; children: React.ReactNode }) {
  return <section className={`operation-detail ${wide ? 'operation-detail-wide' : ''}`}><header><h2>{title}</h2><span>{ready ? '已记录' : '等待中'}</span></header>{ready ? children : <p className="muted">尚无持久化结果</p>}</section>
}

function SummaryState({ positive, positiveText, negativeText }: { positive: boolean; positiveText: string; negativeText: string }) {
  return <span className={`summary-state ${positive ? 'summary-positive' : 'summary-negative'}`}>{positive ? positiveText : negativeText}</span>
}

function Findings({ findings = [] }: { findings?: OperationFinding[] }) {
  if (findings.length === 0) return null
  return <ul className="finding-list">{findings.map((finding) => <li key={`${finding.code}:${finding.summary}`} className={`finding-${finding.severity}`}><strong>{finding.summary}</strong>{finding.detail && <span>{finding.detail}</span>}<code>{finding.code}</code></li>)}</ul>
}

function TargetList({ targets }: { targets: OperationTarget[] }) {
  return <div className="target-list">{targets.map((target, index) => <code key={`${formatTarget(target)}:${index}`}>{formatTarget(target)}</code>)}</div>
}

function BoundaryPanel({ title, code, message, safeNext, manualReview, blocking = false }: { title: string; code: string; message: string; safeNext: string; manualReview: boolean; blocking?: boolean }) {
  const atomicBlocked = blocking && code === atomicExchangeUnavailable
  return <section className={`boundary-panel ${blocking ? 'boundary-panel-prominent' : ''}`}><header><strong>{title}</strong><code>{code}</code></header>{blocking && <h2>{atomicBlocked ? '当前环境不满足该操作的安全执行条件，已停止在实际变更之前。' : '当前操作已停止在实际变更之前。'}</h2>}<p>{message}</p><dl><div><dt>{blocking ? '当前安全下一步' : '安全下一步'}</dt><dd>{safeNext}</dd></div><div><dt>需要人工复核</dt><dd>{manualReview ? '是' : '否'}</dd></div></dl></section>
}

function planningOutcome(run: OperationRun) {
  const codes = collectFindingCodes(run)
  if (run.status.state === 'blocked') {
    if (codes.has(atomicExchangeUnavailable)) return { tone: 'blocked', title: '当前环境不满足该操作的安全执行条件', detail: '系统已停止在实际变更之前，没有把安全能力不足表述为“迁移失败”。' }
    return { tone: 'blocked', title: '操作规划被安全条件阻断', detail: '系统已停止在实际变更之前，请先查看阻断原因和安全下一步。' }
  }
  if (run.status.state === 'canceled_before_apply') return { tone: 'neutral', title: '操作已在实际变更前取消', detail: '没有继续进入 Product Apply。' }
  if (run.status.state === 'succeeded') return { tone: 'ready', title: '受控操作已完成', detail: 'Server 已完成变更并记录验证结果。' }
  if (run.status.state === 'rolled_back') return { tone: 'neutral', title: '受控操作已完成回滚', detail: 'Server 已完成恢复并记录回滚验证结果。' }
  if (run.status.state === 'rollback_failed') return { tone: 'blocked', title: '回滚未能完成，需要人工处理', detail: run.status.recovery?.safe_next_action || '请根据 Server recovery 信息进行安全恢复。' }
  if (run.status.state === 'failed') return { tone: 'blocked', title: '受控操作已失败', detail: run.status.recovery?.safe_next_action || run.status.block?.safe_next_action || '请根据 Server 返回的失败信息确认安全下一步。' }
  if (run.status.state === 'interrupted') return { tone: 'blocked', title: '执行已中断，等待安全处理', detail: run.status.recovery?.safe_next_action || '请先完成安全核对，再决定后续动作。' }
  if (run.status.state === 'awaiting_confirmation') return { tone: 'ready', title: '计划与影响摘要已生成', detail: '可确认当前 exact plan；确认后 Product Apply 仍保持关闭。' }
  if (['confirmed', 'ready_to_apply'].includes(run.status.state)) return { tone: 'ready', title: '当前计划已经确认', detail: '确认只锁定计划摘要，实际变更执行仍未开放。' }
  return { tone: 'progress', title: '正在生成受控操作计划', detail: '系统只进行 Discovery、Precheck、Plan 与 Impact 阶段，不会执行实际变更。' }
}

function collectFindingCodes(run: OperationRun) {
  const codes = new Set<string>()
  if (run.status.block?.code) codes.add(run.status.block.code)
  for (const finding of run.discovery?.findings ?? []) codes.add(finding.code)
  for (const finding of run.precheck?.findings ?? []) codes.add(finding.code)
  for (const finding of run.plan?.findings ?? []) codes.add(finding.code)
  return codes
}

function formatTarget(target: OperationTarget) {
  return [target.kind, target.site_id, target.node_id, target.component, target.resource].filter(Boolean).join(' / ')
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`
  return `${(value / 1024 / 1024).toFixed(1)} MiB`
}

function riskLabel(risk: string) {
  return ({ low: '低风险', medium: '中风险', high: '高风险', critical: '严重风险' } as Record<string, string>)[risk] || risk
}
