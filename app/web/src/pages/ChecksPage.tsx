import { Play, RefreshCw, Search, ShieldCheck } from 'lucide-react'
import { useState } from 'react'
import { api } from '../api/client'
import type { CheckBundle, CheckPolicy, GranularCheckDefinition } from '../api/types'
import { Button, EmptyState, ErrorState, IconButton, Loading, PageHeader } from '../components/ui'
import { useResource } from '../hooks/useResource'

export function ChecksPage({ navigate }: { navigate: (path: string) => void }) {
  const resource = useResource(async (signal) => {
    const [definitions, bundles, policies] = await Promise.all([
      api.checkDefinitions(signal), api.checkBundles(signal), api.checkPolicies(signal),
    ])
    return { definitions: definitions.definitions, bundles: bundles.bundles, policies: policies.policies }
  })
  const [query, setQuery] = useState('')
  const [risk, setRisk] = useState<'all' | GranularCheckDefinition['risk']>('all')
  const [system, setSystem] = useState('all')

  if (resource.loading && !resource.data) return <Loading label="正在读取检查目录" />
  if (resource.error && !resource.data) return <ErrorState message={resource.error} retry={resource.refresh} />
  const { definitions: rawDefinitions, bundles, policies } = resource.data!
  const definitions = rawDefinitions.map(normalizeCheckDefinition)
  const normalizedQuery = query.trim().toLocaleLowerCase('zh-CN')
  const systems = [...new Set(definitions.flatMap((definition) => definition.supported_systems))].sort()
  const filtered = definitions.filter((definition) => {
    const text = [definition.name, definition.id, definition.category, definition.description, definition.recommended_value, ...definition.source_refs]
      .join(' ').toLocaleLowerCase('zh-CN')
    return (!normalizedQuery || text.includes(normalizedQuery)) && (risk === 'all' || definition.risk === risk) && (system === 'all' || definition.supported_systems.includes(system))
  })
  const groups = [...new Set(filtered.map((definition) => definition.category))]
    .sort((left, right) => left.localeCompare(right, 'zh-CN'))
    .map((category) => [category, filtered.filter((definition) => definition.category === category)] as const)
  const relationships = buildRelationships(bundles, policies)

  return <>
    <PageHeader title="检查项" description={`${definitions.length} 个独立检查 · ${bundles.length} 个集合 · ${policies.length} 个策略`} actions={<><IconButton label={resource.loading ? '正在刷新' : '刷新'} disabled={resource.loading} onClick={resource.refresh}><RefreshCw size={17} className={resource.loading ? 'spin' : ''} /></IconButton><Button className="button-primary" onClick={() => navigate('/runs')}><Play size={16} />发起检查</Button></>} />
    {resource.error && <div className="notice-error" role="status">刷新失败，当前仍显示上一次成功目录：{resource.error}</div>}
    <section className="catalog-guide" aria-label="检查层级说明">
      <div><strong>检查项</strong><span>最小执行单元，可独立选择</span></div><span>→</span><div><strong>集合</strong><span>组织一组相关检查项</span></div><span>→</span><div><strong>策略</strong><span>组合检查项与集合</span></div>
      <Button className="button-quiet" onClick={() => navigate('/runs')}>在检查批次中选择并预览范围</Button>
    </section>
    <div className="filter-bar" aria-label="检查项筛选">
      <label className="search-field"><Search size={15} /><span className="sr-only">搜索检查项</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索名称、ID、分类、推荐值或 SourceRef" /></label>
      <label><span>风险</span><select aria-label="检查风险" value={risk} onChange={(event) => setRisk(event.target.value as typeof risk)}><option value="all">全部</option><option value="low">低</option><option value="medium">中</option><option value="high">高</option><option value="critical">严重</option></select></label>
      <label><span>系统</span><select aria-label="适用系统" value={system} onChange={(event) => setSystem(event.target.value)}><option value="all">全部</option>{systems.map((value) => <option key={value} value={value}>{value}</option>)}</select></label>
      <span className="filter-count">显示 {filtered.length} / {definitions.length}</span>
    </div>
    {definitions.length === 0 ? <EmptyState>没有已注册的正式检查</EmptyState> : filtered.length === 0 ? <EmptyState>没有符合当前筛选条件的检查项</EmptyState> : <div className="check-list">
      {groups.map(([category, checks]) => <section className="check-group" key={category}>
        <header><div className="check-title"><span className="check-icon"><ShieldCheck size={18} /></span><div><h2>{category}</h2><p>{checks.length} 个当前可见检查 · 只读执行</p></div></div></header>
        <div className="table-wrap"><table><thead><tr><th>检查项</th><th>推荐值</th><th>风险</th><th>适用系统</th><th>集合 / 策略</th><th>来源</th></tr></thead><tbody>
          {checks.map((check) => <CheckRow key={check.id} check={check} relationships={relationships.get(check.id)} />)}
        </tbody></table></div>
      </section>)}
    </div>}
  </>
}

function normalizeCheckDefinition(check: GranularCheckDefinition): GranularCheckDefinition & { source_refs: string[] } {
  return {
    ...check,
    supported_systems: Array.isArray(check.supported_systems) ? check.supported_systems : [],
    parameters: Array.isArray(check.parameters) ? check.parameters : [],
    source_refs: Array.isArray(check.source_refs) ? check.source_refs : [],
  }
}

function CheckRow({ check, relationships }: { check: GranularCheckDefinition & { source_refs: string[] }; relationships?: { bundles: string[]; policies: string[] } }) {
  return <tr>
    <td className="check-name-cell"><strong>{check.name}</strong><small>{check.id}</small><p>{check.description}</p>{check.parameters.length > 0 && <details className="check-parameters"><summary>参数说明 · {check.parameters.length}</summary><ul>{check.parameters.map((parameter) => <li key={parameter.name}><code>{parameter.name}</code><span>{parameter.type}{parameter.required ? ' · 必填' : ' · 可选'}</span><p>{parameter.description}</p>{parameter.options?.length ? <small>允许值：{parameter.options.join(' / ')}</small> : null}</li>)}</ul><p className="parameter-authority">前端仅提供输入提示，Server 仍是最终参数契约权威。</p></details>}</td>
    <td className="recommended-cell">{check.recommended_value}</td>
    <td><span className={`risk risk-${check.risk}`}>{riskLabel(check.risk)}</span></td>
    <td>{check.supported_systems.join(', ')}</td>
    <td><RelationshipList relationships={relationships} /></td>
    <td><SourceRefs values={check.source_refs} /></td>
  </tr>
}

function RelationshipList({ relationships }: { relationships?: { bundles: string[]; policies: string[] } }) {
  if (!relationships || (!relationships.bundles.length && !relationships.policies.length)) return <span className="muted">独立检查</span>
  return <div className="relationship-list">{relationships.bundles.map((name) => <span key={`bundle:${name}`}>集合 · {name}</span>)}{relationships.policies.map((name) => <span key={`policy:${name}`}>策略 · {name}</span>)}</div>
}

function SourceRefs({ values }: { values: string[] }) {
  if (!values.length) return <span className="muted">内置规则</span>
  return <details className="source-refs"><summary>{values.length} 个来源</summary><ul>{values.map((value) => <li key={value}><code>{value}</code></li>)}</ul></details>
}

function buildRelationships(bundles: CheckBundle[], policies: CheckPolicy[]) {
  const result = new Map<string, { bundles: string[]; policies: string[] }>()
  const bundleByID = new Map(bundles.map((bundle) => [bundle.id, bundle]))
  const ensure = (id: string) => {
    const current = result.get(id) || { bundles: [], policies: [] }
    result.set(id, current)
    return current
  }
  for (const bundle of bundles) {
    for (const checkID of bundle.check_ids) ensure(checkID).bundles.push(bundle.name)
  }
  for (const policy of policies) {
    const checkIDs = new Set(policy.check_ids || [])
    for (const bundleID of policy.bundle_ids || []) bundleByID.get(bundleID)?.check_ids.forEach((checkID) => checkIDs.add(checkID))
    for (const checkID of checkIDs) ensure(checkID).policies.push(policy.name)
  }
  for (const value of result.values()) {
    value.bundles.sort((left, right) => left.localeCompare(right, 'zh-CN'))
    value.policies.sort((left, right) => left.localeCompare(right, 'zh-CN'))
  }
  return result
}

function riskLabel(risk: string) {
  return ({ low: '低', medium: '中', high: '高', critical: '严重' } as Record<string, string>)[risk] || risk
}
