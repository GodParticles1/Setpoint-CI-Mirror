import { ArrowRight, CheckSquare2, Play, RefreshCw, Square } from 'lucide-react'
import { useRef, useState } from 'react'
import { api, APIError } from '../api/client'
import type { CheckBundle, CheckPolicy, CheckSelection, GranularCheckDefinition } from '../api/types'
import { Button, EmptyState, ErrorState, IconButton, Loading, PageHeader, PhaseBadge } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { dateTime, shortID } from '../lib/format'
import { IdempotentOperation } from '../lib/idempotency'
import { ResultCounts } from './DashboardPage'

type CapabilityMode = 'checks' | 'bundles' | 'policies'

export function RunsPage({ navigate }: { navigate: (path: string) => void }) {
  const resource = useResource(async (signal) => {
    const [nodes, definitions, bundles, policies, runs] = await Promise.all([
      api.nodes(signal), api.checkDefinitions(signal), api.checkBundles(signal), api.checkPolicies(signal), api.runs(0, signal),
    ])
    return {
      nodes: nodes.nodes, definitions: definitions.definitions, bundles: bundles.bundles,
      policies: policies.policies, runs: runs.runs,
    }
  })
  const [selectedNodes, setSelectedNodes] = useState<string[]>([])
  const [selection, setSelection] = useState<CheckSelection>({ checkIds: [], bundleIds: [], policyIds: [] })
  const [mode, setMode] = useState<CapabilityMode>('checks')
  const [name, setName] = useState('安全基线检查')
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState('')
  const createOperation = useRef(new IdempotentOperation()).current

  if (resource.loading && !resource.data) return <Loading label="正在读取可执行检查" />
  if (resource.error && !resource.data) return <ErrorState message={resource.error} retry={resource.refresh} />
  const { nodes, definitions, bundles, policies, runs } = resource.data!
  const resolvedCheckIDs = resolveCheckIDs(definitions, bundles, policies, selection)
  const selectedCapabilityCount = selection.checkIds.length + selection.bundleIds.length + selection.policyIds.length
  const pluginCount = new Set(definitions.filter((definition) => resolvedCheckIDs.includes(definition.id)).map((definition) => definition.plugin_id)).size

  const submit = async () => {
    if (!selectedNodes.length || !selectedCapabilityCount) return
    setSubmitting(true)
    setSubmitError('')
    try {
      const normalizedName = name.trim()
      const normalizedSelection = {
        checkIds: [...selection.checkIds].sort(), bundleIds: [...selection.bundleIds].sort(), policyIds: [...selection.policyIds].sort(),
      }
      const fingerprint = JSON.stringify({ name: normalizedName, node_ids: [...selectedNodes].sort(), ...normalizedSelection })
      const run = await api.createRun(normalizedName, selectedNodes, normalizedSelection, createOperation.keyFor(fingerprint))
      createOperation.complete()
      navigate(`/runs/${encodeURIComponent(run.metadata.id)}`)
    } catch (error) {
      setSubmitError(error instanceof APIError ? error.message : '创建检查批次失败')
    } finally {
      setSubmitting(false)
    }
  }

  return <>
    <PageHeader title="检查批次" description="选择在线节点与检查能力后创建只读任务" actions={<IconButton label="刷新" onClick={resource.refresh}><RefreshCw size={17} /></IconButton>} />
    <section className="run-composer">
      <div className="composer-main">
        <label className="field"><span>批次名称</span><input value={name} maxLength={200} onChange={(event) => setName(event.target.value)} /></label>
        <Selection title="节点" count={selectedNodes.length}>
          {nodes.length === 0 ? <EmptyState>暂无已登记节点</EmptyState> : nodes.map((node) => <SelectRow key={node.id} selected={selectedNodes.includes(node.id)} disabled={node.status !== 'online'} onClick={() => setSelectedNodes(toggle(selectedNodes, node.id))}>
            <div><strong>{node.hostname}</strong><small>{node.site_name || '未分配站点'} · {node.os} {node.os_version}</small></div><span className={`node-state node-${node.status}`}>{node.status === 'online' ? '在线' : '离线'}</span>
          </SelectRow>)}
        </Selection>
        <div className="capability-picker">
          <div className="segmented capability-tabs" role="tablist" aria-label="检查能力类型">
            <button type="button" className={mode === 'checks' ? 'active' : ''} onClick={() => setMode('checks')}>检查项 {selection.checkIds.length}</button>
            <button type="button" className={mode === 'bundles' ? 'active' : ''} onClick={() => setMode('bundles')}>集合 {selection.bundleIds.length}</button>
            <button type="button" className={mode === 'policies' ? 'active' : ''} onClick={() => setMode('policies')}>策略 {selection.policyIds.length}</button>
          </div>
          {mode === 'checks' && <Selection title="独立检查项" count={selection.checkIds.length}>{definitions.map((definition) => <SelectRow key={definition.id} selected={selection.checkIds.includes(definition.id)} onClick={() => setSelection({ ...selection, checkIds: toggle(selection.checkIds, definition.id) })}><div><strong>{definition.name}</strong><small>{definition.category} · {definition.id}</small></div><span className="muted">{riskLabel(definition.risk)}</span></SelectRow>)}</Selection>}
          {mode === 'bundles' && <Selection title="检查集合" count={selection.bundleIds.length}>{bundles.map((bundle) => <SelectRow key={bundle.id} selected={selection.bundleIds.includes(bundle.id)} onClick={() => setSelection({ ...selection, bundleIds: toggle(selection.bundleIds, bundle.id) })}><div><strong>{bundle.name}</strong><small>{bundle.category} · {bundle.check_ids.length} 个检查项</small></div><span className="muted">集合</span></SelectRow>)}</Selection>}
          {mode === 'policies' && <Selection title="检查策略" count={selection.policyIds.length}>{policies.map((policy) => <SelectRow key={policy.id} selected={selection.policyIds.includes(policy.id)} onClick={() => setSelection({ ...selection, policyIds: toggle(selection.policyIds, policy.id) })}><div><strong>{policy.name}</strong><small>{policy.description}</small></div><span className="muted">策略</span></SelectRow>)}</Selection>}
        </div>
      </div>
      <aside className="composer-summary"><h2>执行范围</h2><dl><div><dt>在线节点</dt><dd>{selectedNodes.length}</dd></div><div><dt>已选能力</dt><dd>{selectedCapabilityCount}</dd></div><div><dt>覆盖检查项</dt><dd>{resolvedCheckIDs.length}</dd></div><div><dt>预计任务</dt><dd>{selectedNodes.length * pluginCount}</dd></div></dl>{submitError && <p className="inline-error">{submitError}</p>}<Button className="button-primary button-wide" disabled={submitting || !selectedNodes.length || !selectedCapabilityCount || !name.trim()} onClick={submit}><Play size={16} />{submitting ? '正在创建' : '开始检查'}</Button></aside>
    </section>
    <section className="section-block"><div className="section-heading"><div><h2>历史批次</h2><p>最近 {runs.length} 个批次</p></div></div>
      {runs.length === 0 ? <EmptyState>尚未创建检查批次</EmptyState> : <div className="table-wrap"><table><thead><tr><th>批次</th><th>状态</th><th>范围</th><th>结果</th><th>创建时间</th><th /></tr></thead><tbody>
        {runs.map((run) => <tr key={run.metadata.id}><td><strong>{run.metadata.name || '未命名检查'}</strong><small>{shortID(run.metadata.id)}</small></td><td><PhaseBadge phase={run.status.phase} /></td><td>{run.spec.node_ids.length} 节点 · {run.spec.check_ids.length} 检查</td><td><ResultCounts summary={run.status.counts} /></td><td>{dateTime(run.metadata.created_at)}</td><td><IconButton label="查看批次" onClick={() => navigate(`/runs/${encodeURIComponent(run.metadata.id)}`)}><ArrowRight size={17} /></IconButton></td></tr>)}
      </tbody></table></div>}
    </section>
  </>
}

function Selection({ title, count, children }: { title: string; count: number; children: React.ReactNode }) {
  return <fieldset className="selection"><legend>{title}<span>{count} 已选</span></legend><div className="selection-list">{children}</div></fieldset>
}

function SelectRow({ selected, disabled = false, onClick, children }: { selected: boolean; disabled?: boolean; onClick: () => void; children: React.ReactNode }) {
  return <button type="button" className={`select-row ${selected ? 'selected' : ''}`} disabled={disabled} onClick={onClick}>{selected ? <CheckSquare2 size={18} /> : <Square size={18} />}{children}</button>
}

function resolveCheckIDs(
  definitions: GranularCheckDefinition[], bundles: CheckBundle[], policies: CheckPolicy[], selection: CheckSelection,
) {
  const result = new Set(selection.checkIds)
  const bundleMap = new Map(bundles.map((bundle) => [bundle.id, bundle]))
  const addBundle = (id: string) => bundleMap.get(id)?.check_ids.forEach((checkID) => result.add(checkID))
  selection.bundleIds.forEach(addBundle)
  policies.filter((policy) => selection.policyIds.includes(policy.id)).forEach((policy) => {
    policy.check_ids?.forEach((checkID) => result.add(checkID))
    policy.bundle_ids?.forEach(addBundle)
  })
  const known = new Set(definitions.map((definition) => definition.id))
  return [...result].filter((id) => known.has(id)).sort()
}

function toggle(values: string[], value: string) {
  return values.includes(value) ? values.filter((current) => current !== value) : [...values, value]
}

function riskLabel(risk: string) {
  return ({ low: '低风险', medium: '中风险', high: '高风险', critical: '严重风险' } as Record<string, string>)[risk] || risk
}
