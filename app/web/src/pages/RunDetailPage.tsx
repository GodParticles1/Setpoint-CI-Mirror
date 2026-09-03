import { ArrowLeft, Ban, CircleAlert, RefreshCw, Server, Wrench } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { api, APIError } from '../api/client'
import type { CancelReport, CheckItem, CheckRun, ItemStatus, OperationBatchConfirmationResponse, OperationRun, RemediationOffer, TaskResource } from '../api/types'
import { ExecutionActivity } from '../components/ExecutionActivity'
import { Button, ErrorState, IconButton, Loading, PageHeader, PhaseBadge, StatusBadge } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { dateTime, isTerminal, newIdempotencyKey, operationTerminal, shortID } from '../lib/format'
import { operationActivityLabel } from '../lib/operationPresentation'
import { ResultCounts } from './DashboardPage'

type ResultFilter = 'all' | ItemStatus | 'actionable' | 'manual_only'
type ResultRow = { task: TaskResource; item: CheckItem; offer?: RemediationOffer }
type RepairExecution = { run?: OperationRun; error?: string; creating?: boolean; confirming?: boolean; canceling?: boolean }
type FrozenRepairPlan = { runID: string; planDigest: string; planSummary: string; impactSummary: string; risk: string }
type RepairBatchItem = { createKey: string; runID?: string }
type RepairBatchSnapshot = {
  version: 2
  checkRunID: string
  batchID: string
  confirmationKey: string
  rowKeys: string[]
  items: Record<string, RepairBatchItem>
  preview?: Record<string, FrozenRepairPlan>
}

const filters: Array<{ value: ResultFilter; label: string }> = [
  { value: 'all', label: '全部' }, { value: 'actionable', label: '可自动修复' }, { value: 'manual_only', label: '仅人工处理' },
  { value: 'unsafe', label: '不安全' }, { value: 'manual_review', label: '人工复核' }, { value: 'error', label: '错误' },
  { value: 'safe', label: '安全' }, { value: 'not_applicable', label: '不适用' },
]

export function pollDelay(failureCount: number) {
  return Math.min(5_000 * (2 ** Math.min(Math.max(failureCount, 0), 3)), 30_000)
}

export function RunDetailPage({ id, navigate }: { id: string; navigate: (path: string) => void }) {
  const resource = useResource((signal) => api.run(id, signal), [id])
  const nodeResource = useResource((signal) => api.nodes(signal))
  const [filter, setFilter] = useState<ResultFilter>('all')
  const [canceling, setCanceling] = useState(false)
  const [actionError, setActionError] = useState('')
  const [cancelReport, setCancelReport] = useState<CancelReport>()
  const [selectedRepairKeys, setSelectedRepairKeys] = useState<string[]>([])
  const [repairOpen, setRepairOpen] = useState(false)
  const [serverBatch, setServerBatch] = useState<OperationBatchConfirmationResponse>()

  useEffect(() => {
    if (!resource.data || resource.loading || isTerminal(resource.data.status.phase)) return
    const timer = window.setTimeout(resource.refresh, pollDelay(resource.failureCount))
    return () => window.clearTimeout(timer)
  }, [resource.data?.status.phase, resource.data?.status.updated_at, resource.loading, resource.failureCount, resource.settledRevision, resource.refresh])

  const rows = useMemo<ResultRow[]>(() => {
    const run = resource.data
    if (!run) return []
    const offers = new Map((run.remediation_offers ?? []).map((offer) => [offerKey(offer), offer]))
    return (run.tasks ?? []).flatMap((task) => (task.result?.items ?? []).map((item) => ({
      task,
      item,
      offer: offers.get(`${run.metadata.id}:${task.metadata.id}:${item.id}:${task.spec.node_id}`),
    })))
  }, [resource.data])
  const actionableRows = useMemo(() => rows.filter(isActionable), [rows])
  const visible = useMemo(() => filterRows(rows, filter), [rows, filter])
  const selectedRepairs = useMemo(() => rows.filter((row) => selectedRepairKeys.includes(rowKey(row))), [rows, selectedRepairKeys])
  const nodeNames = useMemo(() => new Map((nodeResource.data?.nodes ?? []).map((node) => [node.id, node.hostname])), [nodeResource.data])

  useEffect(() => {
    const valid = new Set(rows.filter(isRepairSelectable).map(rowKey))
    setSelectedRepairKeys((current) => {
      const retained = current.filter((key) => valid.has(key))
      if (retained.length > 0 || valid.size === 0) return retained
      const stored = readRepairBatch(id)
      if (!stored) return retained
      return stored.rowKeys.filter((key) => valid.has(key))
    })
  }, [rows, id])

  useEffect(() => {
    if (rows.length === 0) return
    let cancelled = false
    const valid = new Set(rows.filter(isRepairSelectable).map(rowKey))
    void api.operationBatchConfirmations(id).then(({ confirmations }) => {
      if (cancelled || confirmations.length === 0) return
      const latest = confirmations[0]
      const keys = latest.receipt.members.map((member) => `${member.identity.task_id}:${member.identity.check_id}`).filter((key) => valid.has(key))
      if (keys.length === 0) return
      setServerBatch(latest)
      setSelectedRepairKeys((current) => current.length > 0 ? current : keys)
    }).catch(() => {
      // Batch reconstruction is additive; the CheckRun remains usable if it is temporarily unavailable.
    })
    return () => { cancelled = true }
  }, [id, rows])

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
    } catch (error) {
      setActionError(apiErrorText(error, '取消失败'))
    } finally {
      setCanceling(false)
    }
  }

  const toggleRepair = (row: ResultRow) => {
    const key = rowKey(row)
    setSelectedRepairKeys((current) => current.includes(key) ? current.filter((value) => value !== key) : [...current, key])
  }

  const selectVisibleActionable = () => {
    const keys = visible.filter(isActionable).map(rowKey)
    setSelectedRepairKeys((current) => [...new Set([...current, ...keys])])
  }

  return <>
    <PageHeader title={run.metadata.name || '检查结果'} description={`批次 ${shortID(run.metadata.id)} · ${dateTime(run.metadata.created_at)}`} actions={<><IconButton label="返回批次" onClick={() => navigate('/runs')}><ArrowLeft size={18} /></IconButton><IconButton label="刷新" onClick={resource.refresh}><RefreshCw size={17} /></IconButton>{!isTerminal(run.status.phase) && <Button className="button-danger" disabled={canceling} onClick={cancel}><Ban size={15} />{canceling ? '正在取消' : '取消批次'}</Button>}</>} />
    {actionError && <div className="notice-error">{actionError}</div>}
    {cancelReport && <div className={cancelReport.failed_tasks > 0 ? 'notice-error' : 'notice-info'} role="status">{cancelReport.failed_tasks > 0 ? '批次已部分处理' : '取消请求已处理'}：直接取消 {cancelReport.canceled_tasks}，执行中请求取消 {cancelReport.cancel_requested_tasks}，已结束 {cancelReport.already_terminal_tasks}，失败 {cancelReport.failed_tasks}。</div>}
    {resource.error && <div className="notice-error" role="status">自动刷新失败：{resource.error}。将在 {pollDelay(resource.failureCount) / 1000} 秒后重试。</div>}
    {!isTerminal(run.status.phase) && <ExecutionActivity title="正在执行安全检查" status={checkActivityStatus(run)} active completed={run.status.counts.completed_tasks} total={run.status.counts.total_tasks} />}
    <section className={`result-attention result-attention-${attention.tone}`} aria-label="检查批次结论"><CircleAlert size={18} /><div><strong>{attention.title}</strong><p>{attention.detail}</p></div></section>
    <section className="run-summary"><div><span>状态</span><PhaseBadge phase={run.status.phase} /></div><div><span>节点</span><strong>{run.spec.node_ids.length}</strong></div><div><span>检查项</span><strong>{run.spec.check_ids.length}</strong></div><div><span>任务</span><strong>{run.status.counts.completed_tasks} / {run.status.counts.total_tasks}</strong></div><div className="run-results"><span>检查结论</span><ResultCounts summary={run.status.counts} /></div></section>
    {nodeResource.error && <div className="notice-info">节点名称暂未加载，结果仍使用节点 ID 展示。</div>}
    <div className="segmented" role="group" aria-label="结果筛选">{filters.map((current) => <button key={current.value} className={filter === current.value ? 'active' : ''} onClick={() => setFilter(current.value)}>{current.label}</button>)}</div>
    <section className="section-block result-section">
      <div className="section-heading"><div><h2>检查结果</h2><p>{actionableRows.length} 项由 Server 标记为可自动修复</p></div><div className="page-actions"><Button className="button-quiet" disabled={visible.filter(isActionable).length === 0} onClick={selectVisibleActionable}>选择当前可修复项</Button><Button className="button-quiet" disabled={selectedRepairKeys.length === 0} onClick={() => setSelectedRepairKeys([])}>清空选择</Button><Button className="button-primary" disabled={selectedRepairKeys.length === 0} onClick={() => setRepairOpen(true)}><Wrench size={15} />修复工作区 {selectedRepairKeys.length}</Button></div></div>
      {rows.length === 0 ? <div className="state-block state-empty">{isTerminal(run.status.phase) ? '没有可显示的检查结果' : 'Agent 正在等待或执行任务'}</div> : visible.length === 0 ? <div className="state-block state-empty">当前筛选没有结果</div> : <div className="table-wrap result-table"><table><thead><tr><th>选择</th><th>结论</th><th>节点</th><th>检查项</th><th>当前 / 建议</th><th>判断依据与下一步</th></tr></thead><tbody>
        {visible.map((row) => {
          const { task, item, offer } = row
          const actionable = isActionable(row)
          const selectable = isRepairSelectable(row)
          const key = rowKey(row)
          return <tr key={key} className={`result-row result-row-${item.status}`}>
            <td><input type="checkbox" aria-label={`选择修复 ${item.name}`} checked={selectedRepairKeys.includes(key)} disabled={!selectable} onChange={() => toggleRepair(row)} /></td>
            <td><StatusBadge status={item.status} />{actionable && <small>Server：可修复</small>}{!actionable && offer?.availability === 'manual_only' && <small>Server：仅人工</small>}</td>
            <td><div className="result-node"><Server size={15} /><div><strong>{nodeNames.get(task.spec.node_id) || task.spec.node_id}</strong>{nodeNames.has(task.spec.node_id) && <small title={task.spec.node_id}>节点 ID {shortID(task.spec.node_id)}</small>}</div></div></td>
            <td><strong>{item.name}</strong><small title={item.id}>{item.id}</small><small className="technical-secondary">插件 {task.spec.plugin_id}</small></td>
            <td><div className="value-compare"><span><b>当前</b>{offer?.current_value || item.current_value || '无'}</span><span><b>本次建议</b>{offer?.recommended_value_for_this_run || item.recommended_value || '无'}</span></div></td>
            <td><div className="result-reason"><span className="result-evidence-label">检查证据</span><p>{item.evidence_summary || '没有可显示的证据摘要'}</p>
              {offer?.recommendation_reason && <div className="result-guidance guidance-unsafe"><strong>本次建议原因</strong><p>{offer.recommendation_reason}</p></div>}
              {offer?.availability === 'manual_only' && <div className="result-guidance guidance-review"><strong>无法自动修复</strong><p>{offer.block_reason || 'Server 未授权此结果进入自动修复。'}</p></div>}
              {item.status === 'manual_review' && <div className="result-guidance guidance-review"><strong>为什么需要人工复核</strong><p>{item.review_reason || '现有证据不足以自动得出结论。'}</p></div>}
              {item.status === 'unsafe' && !offer?.recommendation_reason && <div className="result-guidance guidance-unsafe"><strong>建议处理</strong><p>{item.remediation || '请结合当前策略确认处理方式。'}</p></div>}
              {item.status === 'error' && <div className="result-guidance guidance-error"><strong>检查未完成</strong><p>{item.error?.message || '检查执行未得到有效结果。'}</p></div>}
              <ResultSafetyFacts item={item} offer={offer} />
              {item.error && <details><summary>技术详情</summary><code>{item.error.code}: {item.error.message}</code></details>}
            </div></td>
          </tr>
        })}
      </tbody></table></div>}
    </section>
    {repairOpen && <RepairWorkspace checkRunID={id} rows={selectedRepairs} serverBatch={serverBatch} nodeNames={nodeNames} onClose={() => setRepairOpen(false)} onClearSelection={() => { setSelectedRepairKeys([]); setRepairOpen(false); clearRepairBatch(id) }} onRefreshCheckRun={resource.refresh} />}
  </>
}

function ResultSafetyFacts({ item, offer }: { item: CheckItem; offer?: RemediationOffer }) {
  return <details><summary>修复能力与影响</summary><p>检查事实：自动修复 {item.supports_automatic_fix ? '支持' : '不支持'} · 回滚 {item.supports_rollback ? '支持' : '不支持'}</p>{offer && <><p>Server 修复能力：{offer.availability === 'actionable' ? '可执行' : '仅人工'} · 回滚：{offer.supports_rollback ? '支持' : '不支持'} · 风险：{offer.risk}</p><p>重启：{offer.requires_restart ? '需要' : '不需要'} · 连接：{offer.may_affect_connection ? '可能影响' : '无标记'} · 业务：{offer.may_affect_business ? '可能影响' : '无标记'}</p></>}</details>
}

function RepairWorkspace({ checkRunID, rows, serverBatch, nodeNames, onClose, onClearSelection, onRefreshCheckRun }: { checkRunID: string; rows: ResultRow[]; serverBatch?: OperationBatchConfirmationResponse; nodeNames: Map<string, string>; onClose: () => void; onClearSelection: () => void; onRefreshCheckRun: () => void }) {
  const rowKeys = useMemo(() => rows.map(rowKey).sort(), [rows])
  const restored = useMemo(() => {
    const value = readRepairBatch(checkRunID)
    return value && sameKeys(value.rowKeys, rowKeys) ? value : undefined
  }, [checkRunID, rowKeys.join('|')])
  const [batch, setBatch] = useState<RepairBatchSnapshot | undefined>(restored)
  const [executions, setExecutions] = useState<Record<string, RepairExecution>>({})
  const [draftValues, setDraftValues] = useState<Record<string, string>>(() => Object.fromEntries(rows.map((row) => [rowKey(row), row.offer?.recommended_value_for_this_run ?? ''])))
  const [confirmingBatch, setConfirmingBatch] = useState(false)
  const [batchError, setBatchError] = useState('')
  const [cancelingBatch, setCancelingBatch] = useState(false)
  const refreshedSucceeded = useRef(new Set<string>())

  useEffect(() => {
    setDraftValues((current) => {
      const next = { ...current }
      for (const row of rows) if (!(rowKey(row) in next)) next[rowKey(row)] = row.offer?.recommended_value_for_this_run ?? ''
      return next
    })
  }, [rows])

  useEffect(() => {
    if (!serverBatch) return
    const actionableKeys = rows.filter(isActionable).map(rowKey).sort()
    const serverKeys = serverBatch.receipt.members.map((member) => `${member.identity.task_id}:${member.identity.check_id}`).sort()
    if (!sameKeys(actionableKeys, serverKeys)) return
    const runByID = new Map(serverBatch.runs.map((run) => [run.metadata.id, run]))
    const items: Record<string, RepairBatchItem> = {}
    const preview: Record<string, FrozenRepairPlan> = {}
    const recoveredExecutions: Record<string, RepairExecution> = {}
    for (const member of serverBatch.receipt.members) {
      const key = `${member.identity.task_id}:${member.identity.check_id}`
      const run = runByID.get(member.run_id)
      items[key] = { createKey: `server:${member.run_id}`, runID: member.run_id }
      preview[key] = {
        runID: member.run_id, planDigest: member.plan_digest,
        planSummary: run?.plan?.summary || 'Server receipt 已冻结 child plan_digest',
        impactSummary: run?.impact?.summary || 'Server 未提供额外影响摘要',
        risk: run?.impact?.risk || rows.find((row) => rowKey(row) === key)?.offer?.risk || 'unknown',
      }
      if (run) recoveredExecutions[key] = { run }
    }
    const recovered: RepairBatchSnapshot = {
      version: 2, checkRunID, batchID: serverBatch.receipt.batch_id, confirmationKey: serverBatch.receipt.confirmation_idempotency_key,
      rowKeys: rows.map(rowKey).sort(), items, preview,
    }
    setBatch(recovered)
    setExecutions((current) => ({ ...recoveredExecutions, ...current }))
  }, [serverBatch, checkRunID, rows])

  useEffect(() => {
    if (!batch) return
    writeRepairBatch(batch)
  }, [batch])

  useEffect(() => {
    if (!batch) return
    const runEntries = Object.entries(batch.items).filter(([, item]) => item.runID)
    if (runEntries.length === 0) return
    let cancelled = false
    void Promise.all(runEntries.map(async ([key, item]) => {
      try {
        const run = await api.operationRun(item.runID!)
        return { key, run }
      } catch (error) {
        return { key, error: apiErrorText(error, '恢复修复批次失败') }
      }
    })).then((updates) => {
      if (cancelled) return
      setExecutions((current) => {
        const next = { ...current }
        for (const update of updates) next[update.key] = update.run ? { ...next[update.key], run: update.run, error: '' } : { ...next[update.key], error: update.error }
        return next
      })
    })
    return () => { cancelled = true }
  }, [batch?.batchID])

  useEffect(() => {
    const active = Object.entries(executions).filter(([, execution]) => execution.run && !repairPollingTerminal(execution.run.status.state))
    if (active.length === 0) return
    const timer = window.setTimeout(async () => {
      const updates = await Promise.all(active.map(async ([key, execution]) => {
        try {
          const run = await api.operationRun(execution.run!.metadata.id)
          return { key, run }
        } catch (error) {
          return { key, error: apiErrorText(error, '刷新修复状态失败') }
        }
      }))
      let refreshCheck = false
      for (const update of updates) {
        if (update.run?.status.state === 'succeeded' && !refreshedSucceeded.current.has(update.run.metadata.id)) {
          refreshedSucceeded.current.add(update.run.metadata.id)
          refreshCheck = true
        }
      }
      setExecutions((current) => {
        const next = { ...current }
        for (const update of updates) {
          if (update.run) next[update.key] = { ...next[update.key], run: update.run, error: '' }
          else if (update.error) next[update.key] = { ...next[update.key], error: update.error }
        }
        return next
      })
      if (refreshCheck) onRefreshCheckRun()
    }, 3_000)
    return () => window.clearTimeout(timer)
  }, [executions, onRefreshCheckRun])

  useEffect(() => {
    if (!batch || batch.preview) return
    const actionable = rows.filter(isActionable)
    if (actionable.length === 0) return
    const states = actionable.map((row) => executions[rowKey(row)]?.run)
    if (states.some((run) => !run || planningPending(run))) return
    const preview: Record<string, FrozenRepairPlan> = {}
    for (const row of actionable) {
      const key = rowKey(row)
      const run = executions[key]?.run
      if (!run || run.status.state !== 'awaiting_confirmation' || !run.plan_digest) continue
      preview[key] = {
        runID: run.metadata.id,
        planDigest: run.plan_digest,
        planSummary: run.plan?.summary || 'Server 未提供计划摘要',
        impactSummary: run.impact?.summary || 'Server 未提供额外影响摘要',
        risk: run.impact?.risk || row.offer?.risk || 'unknown',
      }
    }
    setBatch((current) => current ? { ...current, preview } : current)
  }, [batch, executions, rows])

  const resetRecommendations = () => setDraftValues(Object.fromEntries(rows.map((row) => [rowKey(row), row.offer?.recommended_value_for_this_run ?? ''])))

  const ensureBatch = () => {
    if (batch && sameKeys(batch.rowKeys, rowKeys)) return batch
    const items = Object.fromEntries(rows.filter(isActionable).map((row) => [rowKey(row), { createKey: newIdempotencyKey() }]))
    const created: RepairBatchSnapshot = { version: 2, checkRunID, batchID: newIdempotencyKey(), confirmationKey: newIdempotencyKey(), rowKeys, items }
    writeRepairBatch(created)
    setBatch(created)
    return created
  }

  const startOne = async (row: ResultRow, snapshot: RepairBatchSnapshot) => {
    const key = rowKey(row)
    const offer = row.offer
    if (!offer || offer.availability !== 'actionable' || !offer.operation_id || !offer.operation_parameters) {
      setExecutions((current) => ({ ...current, [key]: { ...current[key], error: offer?.block_reason || 'Server 未提供完整的可执行修复绑定。' } }))
      return
    }
    const draft = draftValues[key] ?? offer.recommended_value_for_this_run
    if (offer.editable && draft !== offer.recommended_value_for_this_run) {
      setExecutions((current) => ({ ...current, [key]: { ...current[key], error: 'Server contract 未提供可编辑值到 operation_parameters 的安全绑定，已阻止提交。' } }))
      return
    }
    const item = snapshot.items[key]
    if (item?.runID) {
      try {
        const run = await api.operationRun(item.runID)
        setExecutions((current) => ({ ...current, [key]: { run, creating: false, error: '' } }))
        return
      } catch {
        // Reuse the persisted create key below; Server idempotency remains authoritative.
      }
    }
    setExecutions((current) => ({ ...current, [key]: { ...current[key], creating: true, error: '' } }))
    try {
      const run = await api.createOperationRun(offer.operation_id, offer.node_id, [{ kind: 'node', node_id: offer.node_id }], offer.operation_parameters, [], item.createKey)
      setExecutions((current) => ({ ...current, [key]: { run, creating: false, error: '' } }))
      setBatch((current) => {
        if (!current) return current
        const next = { ...current, items: { ...current.items, [key]: { ...current.items[key], runID: run.metadata.id } } }
        writeRepairBatch(next)
        return next
      })
    } catch (error) {
      setExecutions((current) => ({ ...current, [key]: { ...current[key], creating: false, error: apiErrorText(error, '创建修复操作失败') } }))
    }
  }

  const startRecommended = async () => {
    resetRecommendations()
    const snapshot = ensureBatch()
    await Promise.all(rows.filter(isActionable).map((row) => startOne(row, snapshot)))
  }

  const confirmAll = async () => {
    if (!batch?.preview || Object.keys(batch.preview).length === 0) return
    const ordered = batch.rowKeys.flatMap((key) => {
      const frozen = batch.preview?.[key]
      const row = rows.find((candidate) => rowKey(candidate) === key)
      if (!frozen || !row || !isActionable(row)) return []
      return [{ task_id: row.task.metadata.id, check_id: row.item.id, node_id: row.task.spec.node_id, run_id: frozen.runID, plan_digest: frozen.planDigest }]
    })
    if (ordered.length === 0) return
    setBatchError('')
    setConfirmingBatch(true)
    setExecutions((current) => Object.fromEntries(Object.entries(current).map(([key, execution]) => [key, { ...execution, confirming: Boolean(batch.preview?.[key]), error: '' }])))
    try {
      const response = await api.confirmOperationBatch(batch.batchID, checkRunID, batch.confirmationKey, ordered)
      const memberByRun = new Map(response.receipt.members.map((member) => [member.run_id, member]))
      setExecutions((current) => {
        const next = { ...current }
        for (const run of response.runs) {
          const member = memberByRun.get(run.metadata.id)
          if (!member) continue
          const key = `${member.identity.task_id}:${member.identity.check_id}`
          next[key] = { ...next[key], run, confirming: false, error: '' }
        }
        return next
      })
    } catch (error) {
      setExecutions((current) => Object.fromEntries(Object.entries(current).map(([key, execution]) => [key, { ...execution, confirming: false }])))
      setBatchError(apiErrorText(error, '确认批量修复计划失败'))
    } finally {
      setConfirmingBatch(false)
    }
  }

  const cancelRemaining = async () => {
    const targets = Object.entries(executions).filter(([, execution]) => execution.run && !operationTerminal(execution.run.status.state) && execution.run.status.state !== 'interrupted')
    if (targets.length === 0) return
    setCancelingBatch(true)
    await Promise.all(targets.map(async ([key, execution]) => {
      setExecutions((current) => ({ ...current, [key]: { ...current[key], canceling: true, error: '' } }))
      try {
        const run = await api.cancelOperationRun(execution.run!.metadata.id)
        setExecutions((current) => ({ ...current, [key]: { ...current[key], run, canceling: false, error: '' } }))
      } catch (error) {
        setExecutions((current) => ({ ...current, [key]: { ...current[key], canceling: false, error: apiErrorText(error, '取消子操作失败') } }))
      }
    }))
    setCancelingBatch(false)
  }

  const actionable = rows.filter(isActionable)
  const manualOnly = rows.filter((row) => !isActionable(row))
  const previewReady = Boolean(batch?.preview)
  const readyPlans = Object.keys(batch?.preview ?? {}).length
  const anyCreating = Object.values(executions).some((execution) => execution.creating)
  const summary = aggregateRepairStatus(actionable, executions)

  return <div className="dialog-layer" role="dialog" aria-modal="true" aria-labelledby="repair-workspace-title"><button className="dialog-scrim" aria-label="关闭修复工作区" onClick={onClose} /><section className="dialog"><header><h2 id="repair-workspace-title">修复工作区</h2></header><div className="dialog-body">
    <div className="notice-info" role="status">这是一个批量 UX 确认，不是共享事务。每个选中可修复项仍创建独立 OperationRun；lock、plan_digest、RestorePoint、Apply、Verify、Rollback 与 VerifyRollback 均以各 child 的 Server 状态为准。</div>
    <div className="page-actions"><Button className="button-quiet" onClick={resetRecommendations}>恢复系统建议</Button><Button className="button-quiet" onClick={onClearSelection}>取消当前修复选择</Button></div>
    {rows.length === 0 ? <p className="muted">当前没有已选择的修复项。</p> : <>
      <section className="operation-detail operation-detail-wide" aria-label="批量修复预览">
        <header><h2>批量修复预览</h2><span>{actionable.length} 可修复 · {manualOnly.length} 排除</span></header>
        <div className="table-wrap"><table><thead><tr><th>节点</th><th>Check / finding</th><th>当前值</th><th>建议值</th><th>风险</th><th>重启</th><th>连接</th><th>业务</th><th>回滚</th><th>计划 / 排除原因</th></tr></thead><tbody>{rows.map((row) => {
          const key = rowKey(row)
          const offer = row.offer
          const frozen = batch?.preview?.[key]
          const currentRun = executions[key]?.run
          const stale = Boolean(frozen && currentRun?.plan_digest && currentRun.plan_digest !== frozen.planDigest)
          return <tr key={key}><td>{nodeNames.get(row.task.spec.node_id) || row.task.spec.node_id}</td><td><strong>{row.item.name}</strong><br /><small>{row.item.id}</small></td><td>{offer?.current_value || row.item.current_value || '无'}</td><td>{offer?.recommended_value_for_this_run || row.item.recommended_value || '无'}</td><td>{offer?.risk || '—'}</td><td>{offer?.requires_restart ? '需要' : '不需要'}</td><td>{offer?.may_affect_connection ? '可能影响' : '无标记'}</td><td>{offer?.may_affect_business ? '可能影响' : '无标记'}</td><td>{offer?.supports_rollback ? '支持' : '不支持'}</td><td>{isActionable(row) ? frozen ? <><span>{frozen.planSummary}</span><br /><small><code>{shortID(frozen.planDigest)}</code>{stale ? ' · 预览后计划已变化；确认将由 Server fail closed' : ''}</small></> : currentRun?.status.state === 'awaiting_confirmation' ? '等待其余 child 规划完成后冻结统一预览' : executionPreviewText(executions[key]) : <strong>{offer?.block_reason || row.item.review_reason || row.item.error?.message || 'Server 未授权自动修复。'}</strong>}</td></tr>
        })}</tbody></table></div>
      </section>
      {previewReady && <div className="notice-info" role="status"><strong>已冻结一次批量预览。</strong> 本次确认只提交上表冻结的 child plan_digest；轮询不会替换它们。若任一 child 在预览后变旧，Server 会在持久化授权前拒绝整个确认集合，本次确认不会新增任何 child 执行。</div>}
      {batchError && <div className="notice-error" role="alert">{batchError}</div>}
      <div className={summary.tone === 'error' ? 'notice-error' : 'notice-info'} role="status"><strong>{summary.title}</strong><p>{summary.detail}</p></div>
      <div className="repair-workspace-list">{rows.filter(isActionable).map((row) => <RepairItem key={rowKey(row)} row={row} nodeName={nodeNames.get(row.task.spec.node_id) || row.task.spec.node_id} value={draftValues[rowKey(row)] ?? ''} setValue={(value) => setDraftValues((current) => ({ ...current, [rowKey(row)]: value }))} execution={executions[rowKey(row)]} />)}</div>
    </>}
    <div className="dialog-actions"><Button onClick={onClose}>关闭</Button><Button className="button-danger" disabled={cancelingBatch || Object.values(executions).every((execution) => !execution.run || operationTerminal(execution.run.status.state) || execution.run.status.state === 'interrupted')} onClick={cancelRemaining}>{cancelingBatch ? '正在取消' : '取消剩余子操作'}</Button><span /><Button className="button-quiet" disabled={actionable.length === 0 || anyCreating || previewReady} onClick={startRecommended}>{batch ? '重试 / 恢复批量预览' : '生成批量修复预览'}</Button><Button className="button-primary" disabled={!previewReady || readyPlans === 0 || confirmingBatch || Object.values(executions).some((execution) => execution.creating)} onClick={confirmAll}>{confirmingBatch ? 'CONFIRMING SELECTED REPAIR PLANS' : 'CONFIRM ALL SELECTED REPAIR PLANS'}</Button></div>
  </div></section></div>
}

function RepairItem({ row, nodeName, value, setValue, execution }: { row: ResultRow; nodeName: string; value: string; setValue: (value: string) => void; execution?: RepairExecution }) {
  const offer = row.offer
  if (!offer) return <section className="operation-detail"><header><h2>{row.item.name}</h2><span>仅人工</span></header><div className="notice-error">当前结果没有匹配的 Server RemediationOffer，已阻止自动修复。</div></section>
  const run = execution?.run
  return <section className="operation-detail operation-detail-wide">
    <header><h2>{row.item.name}</h2><span>{offer.availability === 'actionable' ? '独立 OperationRun' : '仅人工'}</span></header>
    <dl className="impact-facts"><div><dt>节点</dt><dd>{nodeName}</dd></div><div><dt>当前值</dt><dd>{offer.current_value || '无'}</dd></div><div><dt>原策略建议</dt><dd>{offer.existing_recommended_value || '无'}</dd></div><div><dt>本次建议</dt><dd>{offer.recommended_value_for_this_run || '无'}</dd></div></dl>
    <p>{offer.recommendation_reason || 'Server 未提供额外建议原因。'}</p>
    {offer.availability === 'manual_only' && <div className="notice-error">{offer.block_reason || 'Server 未授权自动修复。'}</div>}
    {offer.availability === 'actionable' && <><div className="operation-boundary"><div><strong>{offer.editable ? 'Server 允许编辑目标值' : '固定建议 / 不可编辑'}</strong><p>{constraintText(offer)}</p></div></div>{offer.editable && <RemediationEditor offer={offer} value={value} setValue={setValue} />}</>}
    <p className="muted">风险：{offer.risk} · 回滚：{offer.supports_rollback ? '支持' : '不支持'} · 重启：{offer.requires_restart ? '需要' : '不需要'} · 连接：{offer.may_affect_connection ? '可能影响' : '无标记'} · 业务：{offer.may_affect_business ? '可能影响' : '无标记'}</p>
    {execution?.creating && <div className="notice-info">正在创建独立 OperationRun，并等待 Server 生成规划证据。</div>}
    {execution?.error && <div className="notice-error" role="alert">{execution.error}</div>}
    {run && !operationTerminal(run.status.state) && <ExecutionActivity title="修复操作" status={operationActivityLabel(run.status.state)} active compact />}
    {run && <RepairExecutionView run={run} />}
    <details><summary>技术详情</summary><p><code>{offer.operation_id || 'no operation binding'}</code></p>{run && <p>OperationRun <code>{shortID(run.metadata.id)}</code> · checkpoint <code>{run.status.checkpoint}</code></p>}</details>
  </section>
}

function RemediationEditor({ offer, value, setValue }: { offer: RemediationOffer; value: string; setValue: (value: string) => void }) {
  const options = offer.constraints.options ?? []
  if (options.length > 0) return <label className="field"><span>目标值</span><select value={value} onChange={(event) => setValue(event.target.value)}>{options.map((option) => <option key={option} value={option}>{option}</option>)}</select></label>
  const numeric = offer.parameter_type === 'integer' || offer.parameter_type === 'number'
  return <label className="field"><span>目标值</span><input type={numeric ? 'number' : 'text'} min={offer.constraints.min} max={offer.constraints.max} pattern={offer.constraints.pattern || undefined} value={value} required onChange={(event) => setValue(event.target.value)} /></label>
}

function RepairExecutionView({ run }: { run: OperationRun }) {
  const status = repairStatus(run)
  return <div className={status.tone === 'error' ? 'notice-error' : 'notice-info'} role="status"><strong>{status.title}</strong><p>{status.detail}</p>
    {run.plan && <div><strong>真实 child 计划</strong><p>{run.plan.summary}</p>{run.impact && <p>影响：{run.impact.summary} · 风险 {run.impact.risk}</p>}</div>}
    {run.execution?.restore_point && <p>恢复点：{run.execution.restore_point.status === 'verified' ? '已创建并验证' : run.execution.restore_point.status}</p>}
    {run.execution?.apply && <p>Apply：{run.execution.apply.changed ? '已执行变更' : '未发生变更'} · {run.execution.apply.checkpoint}</p>}
    {run.execution?.verification && <p>Verify：{run.execution.verification.passed ? '通过' : '未通过'} · {run.execution.verification.summary}</p>}
    {run.execution?.rollback && <p>Rollback：{run.execution.rollback.restored ? '已恢复' : '未确认恢复'} · {run.execution.rollback.checkpoint}</p>}
    {run.execution?.rollback_verification && <p>VerifyRollback：{run.execution.rollback_verification.passed ? '通过' : '未通过'} · {run.execution.rollback_verification.summary}</p>}
    {run.status.block && <p>{run.status.block.code}: {run.status.block.message}</p>}
    {run.status.recovery && <p>{run.status.recovery.code}: {run.status.recovery.safe_next_action}</p>}
  </div>
}

function repairStatus(run: OperationRun) {
  const state = run.status.state
  if (state === 'creating_restore_point') return { tone: 'progress', title: '正在创建恢复点', detail: 'Server 已确认该 child 计划并进入恢复点阶段。' }
  if (state === 'running') return { tone: 'progress', title: '正在修复', detail: 'Server 正在执行该 child 的 bounded Apply action。' }
  if (state === 'verifying') return { tone: 'progress', title: '正在验证', detail: 'Server 正在验证该 child 修复结果。' }
  if (state === 'rolling_back' && run.status.checkpoint.includes('verify_rollback')) return { tone: 'progress', title: '正在验证回滚', detail: 'Server 正在验证恢复后的状态。' }
  if (state === 'rolling_back') return { tone: 'progress', title: '验证未通过，正在自动回滚', detail: 'Rollback 由 Server lifecycle 决定并执行，浏览器未发起回滚。' }
  if (state === 'succeeded') return { tone: 'success', title: '修复成功', detail: 'Server 已完成 Apply 与 Verify。当前检查结果会重新从 Server 获取，不会在浏览器中改写为 safe。' }
  if (state === 'rolled_back') return { tone: 'success', title: '已安全恢复', detail: 'Server 已完成 Rollback 与 VerifyRollback。' }
  if (state === 'rollback_failed') return { tone: 'error', title: '自动回滚未能完成，需要人工恢复', detail: run.status.recovery?.safe_next_action || '请按 Server recovery 信息人工处理。' }
  if (state === 'interrupted') return { tone: 'error', title: '修复结果不确定，需要人工确认', detail: run.status.recovery?.safe_next_action || 'Server 要求先 reconcile，再决定后续动作。' }
  if (state === 'blocked') return { tone: 'error', title: '修复已被 Server 阻断', detail: run.status.block?.message || '当前安全条件不允许继续。' }
  if (state === 'failed') return { tone: 'error', title: '修复动作失败，等待 Server 决定安全下一步', detail: '前端不会自动重试 Apply，也不会自行触发 Rollback。' }
  if (state === 'canceled_before_apply') return { tone: 'error', title: '执行前已取消', detail: '该 child 未进入 Apply。' }
  if (state === 'awaiting_confirmation') return { tone: 'progress', title: '计划已就绪，等待批量确认', detail: '该 child plan_digest 将被纳入一次冻结的批量预览。' }
  return { tone: 'progress', title: '正在准备修复计划', detail: `Server 状态：${state}` }
}

function aggregateRepairStatus(rows: ResultRow[], executions: Record<string, RepairExecution>) {
  const counts = { succeeded: 0, rolledBack: 0, failed: 0, canceled: 0, manualReview: 0, active: 0, pending: 0 }
  for (const row of rows) {
    const run = executions[rowKey(row)]?.run
    if (!run) { counts.pending++; continue }
    switch (run.status.state) {
      case 'succeeded': counts.succeeded++; break
      case 'rolled_back': counts.rolledBack++; break
      case 'rollback_failed': case 'blocked': counts.failed++; break
      case 'canceled_before_apply': counts.canceled++; break
      case 'interrupted': counts.manualReview++; break
      case 'failed': case 'rolling_back': case 'queued': case 'acquiring_lock': case 'creating_restore_point': case 'running': case 'verifying': counts.active++; break
      default: counts.pending++
    }
  }
  const total = rows.length
  if (total > 0 && counts.succeeded === total) return { tone: 'info', title: `全部 ${total} 个 child 修复成功`, detail: `${counts.succeeded} succeeded · 0 rolled back · 0 failed · 0 canceled。` }
  const finalish = counts.succeeded + counts.rolledBack + counts.failed + counts.canceled + counts.manualReview === total && total > 0
  const title = finalish ? '批量修复已得到独立 child 结果' : '批量修复进度'
  const tone = counts.failed > 0 || counts.manualReview > 0 ? 'error' : 'info'
  return { tone, title, detail: `${total} selected · ${counts.succeeded} succeeded · ${counts.rolledBack} rolled back · ${counts.failed} failed · ${counts.canceled} canceled · ${counts.manualReview} manual review · ${counts.active} active · ${counts.pending} pending。` }
}

function executionPreviewText(execution?: RepairExecution) {
  if (execution?.creating) return '正在创建 child OperationRun'
  if (execution?.error) return execution.error
  if (execution?.run) return `Server 状态：${execution.run.status.state}`
  return '尚未生成 child OperationRun'
}

function planningPending(run: OperationRun) {
  return ['draft', 'discovering', 'prechecking', 'planned'].includes(run.status.state)
}

function constraintText(offer: RemediationOffer) {
  const parts: string[] = []
  if (offer.constraints.options?.length) parts.push(`允许值：${offer.constraints.options.join('、')}`)
  if (offer.constraints.min !== undefined) parts.push(`最小值：${offer.constraints.min}`)
  if (offer.constraints.max !== undefined) parts.push(`最大值：${offer.constraints.max}`)
  if (offer.constraints.pattern) parts.push(`格式：${offer.constraints.pattern}`)
  return parts.join('；') || 'Server 未暴露额外约束。'
}

function isActionable(row: ResultRow) {
  return row.offer?.availability === 'actionable'
}

function isManualOnly(row: ResultRow) {
  if (row.item.status === 'manual_review' || row.item.status === 'error') return true
  return row.item.status === 'unsafe' && row.offer?.availability !== 'actionable'
}

function isRepairSelectable(row: ResultRow) {
  return isActionable(row) || isManualOnly(row)
}

function filterRows(rows: ResultRow[], filter: ResultFilter) {
  if (filter === 'all') return rows
  if (filter === 'actionable') return rows.filter(isActionable)
  if (filter === 'manual_only') return rows.filter(isManualOnly)
  return rows.filter(({ item }) => item.status === filter)
}

function rowKey({ task, item }: ResultRow) {
  return `${task.metadata.id}:${item.id}`
}

function offerKey(offer: RemediationOffer) {
  return `${offer.check_run_id}:${offer.task_id}:${offer.check_id}:${offer.node_id}`
}

function repairBatchStorageKey(checkRunID: string) {
  return `setpoint.bulk-remediation.v1:${checkRunID}`
}

function readRepairBatch(checkRunID: string): RepairBatchSnapshot | undefined {
  try {
    const raw = window.localStorage.getItem(repairBatchStorageKey(checkRunID))
    if (!raw) return undefined
    const parsed = JSON.parse(raw) as RepairBatchSnapshot
    if (parsed.version !== 2 || parsed.checkRunID !== checkRunID || !parsed.batchID || !parsed.confirmationKey || !Array.isArray(parsed.rowKeys) || !parsed.items) return undefined
    return parsed
  } catch {
    return undefined
  }
}

function writeRepairBatch(batch: RepairBatchSnapshot) {
  try { window.localStorage.setItem(repairBatchStorageKey(batch.checkRunID), JSON.stringify(batch)) } catch { /* persistence is best-effort; Server receipt and child truth remain authoritative */ }
}

function clearRepairBatch(checkRunID: string) {
  try { window.localStorage.removeItem(repairBatchStorageKey(checkRunID)) } catch { /* no-op */ }
}

function sameKeys(left: string[], right: string[]) {
  return left.length === right.length && left.every((value, index) => value === right[index])
}

function repairPollingTerminal(state: OperationRun['status']['state']) {
  return operationTerminal(state) || state === 'interrupted'
}

function apiErrorText(error: unknown, fallback: string) {
  if (error instanceof APIError) return `${error.message}（${error.code}）`
  return fallback
}

function checkActivityStatus(run: CheckRun) {
  if (run.status.phase === 'pending') return '等待 Agent'
  if (run.status.counts.running_tasks > 0) return '执行中'
  if (run.status.counts.pending_tasks > 0) return '等待 Agent'
  return '正在汇总结果'
}

function resultAttention(phase: CheckRun['status']['phase'], counts: { unsafe: number; manual_review: number; error: number; not_applicable: number }) {
  if (!isTerminal(phase)) return { tone: 'running', title: '检查仍在执行', detail: '结果会随 Agent 完成任务持续更新。' }
  if (counts.error > 0) return { tone: 'error', title: `${counts.error} 个检查没有得到有效结果`, detail: '先查看“检查未完成”的原因；这些结果不能作为安全结论。' }
  if (counts.unsafe > 0) return { tone: 'unsafe', title: `${counts.unsafe} 个不安全项需要关注`, detail: '按节点查看当前值、Server 本次建议和处理能力。' }
  if (counts.manual_review > 0) return { tone: 'review', title: `${counts.manual_review} 个结果需要人工复核`, detail: '这些结果没有被计为安全项，请查看具体复核原因。' }
  return { tone: 'clear', title: '本批次没有发现需要优先处理的异常', detail: counts.not_applicable > 0 ? `${counts.not_applicable} 个检查不适用于当前环境。` : '所有已完成检查均已有明确结论。' }
}
