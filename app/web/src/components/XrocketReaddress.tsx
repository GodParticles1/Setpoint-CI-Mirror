import { ArrowRight, Check, CircleAlert, Clock3, Network, RotateCcw, ShieldCheck } from 'lucide-react'
import type { OperationFinding, OperationRun } from '../api/types'
import { OperationStateBadge } from './ui'

export const xrocketReaddressOperationID = 'xrocket.site.readdress'

type UnknownRecord = Record<string, unknown>

type CurrentSiteState = {
  version: string
  topology: string
  master: string
  slave: string
  vip: string
  prefix: string
  gateway: string
  unresolved: boolean
}

type TargetSiteState = Omit<CurrentSiteState, 'version' | 'topology' | 'unresolved'>

export function XrocketReaddressComposerShell() {
  return <section className="xrocket-composer-shell" aria-label="xRocket 站点改址流程">
    <header><Network size={19} /><div><h3>站点当前状态</h3><p>当前 Master、Slave、VIP、网段、网关、版本与拓扑必须由 Agent Discovery 返回；创建计划前不在浏览器中预填或推测。</p></div><span>等待 Precheck</span></header>
    <div className="xrocket-current-placeholder">
      {['Master', 'Slave', 'VIP', 'Prefix / Gateway', 'Version / Topology'].map((label) => <div key={label}><span>{label}</span><strong>待发现</strong></div>)}
    </div>
    <ol className="xrocket-flow-preview" aria-label="受控生命周期">
      {['Discovery', 'Precheck', 'Diff / Impact', 'Confirm', 'Apply', 'Verify', 'Rollback', 'Final'].map((label, index) => <li key={label}><span>{index + 1}</span><strong>{label}</strong></li>)}
    </ol>
  </section>
}

export function XrocketReaddressRunClosure({ run }: { run: OperationRun }) {
  if (run.spec.operation_id !== xrocketReaddressOperationID) return null
  const current = currentSiteState(run)
  const target = targetSiteState(run)
  const resolved = Boolean(run.discovery?.applicable && run.precheck?.passed && !current.unresolved)
  const changes = run.impact?.changes ?? []

  return <section className="xrocket-run-closure" aria-label="xRocket 站点改址闭环">
    <header className="xrocket-run-heading"><div><span>xRocket Site Readdress</span><h2>站点地址变更</h2><p>只展示 Server 持久化的 Discovery、Precheck、Impact 与 execution 结果。</p></div><OperationStateBadge state={run.status.state} /></header>
    {!resolved && <div className="xrocket-blocked" role="status"><CircleAlert size={18} /><div><strong>当前环境信息尚未解析，禁止进入实际变更</strong><p>必须先取得明确的版本、拓扑和当前地址，并通过 Precheck。页面不会用目标输入代替当前状态。</p></div></div>}
    <div className="xrocket-state-band">
      <SiteState title="当前站点" current={current} />
      <ArrowRight className="xrocket-state-arrow" size={22} aria-hidden="true" />
      <TargetState target={target} />
    </div>
    <section className="xrocket-precheck">
      <header><div><span>Precheck</span><h3>{run.precheck ? run.precheck.passed ? '前置检查通过' : '前置检查未通过' : '等待前置检查'}</h3></div>{run.precheck?.passed ? <ShieldCheck size={20} /> : <CircleAlert size={20} />}</header>
      <p>{run.precheck?.summary || 'Server 尚未返回 Precheck 结果。'}</p>
      <FindingSummary findings={run.precheck?.findings} />
    </section>
    <section className="xrocket-diff">
      <header><div><span>Current → Target Diff</span><h3>地址与网络变化</h3></div><strong>{changes.length} 项 Server 差异</strong></header>
      {changes.length > 0 ? <div className="table-wrap"><table><thead><tr><th>对象</th><th>当前值</th><th>目标值</th><th>风险</th></tr></thead><tbody>{changes.map((change, index) => <tr key={`${change.target.resource || change.target.component || index}:${index}`}><td>{change.target.resource || change.target.component || change.target.kind}</td><td>{change.before || '未提供'}</td><td>{change.after || '未提供'}</td><td><span className={`risk risk-${change.risk}`}>{change.risk}</span></td></tr>)}</tbody></table></div> : <p className="muted">Server 尚未返回可审查的 current → target 差异。</p>}
    </section>
    <section className="xrocket-impact">
      <header><div><span>Impact / Risk</span><h3>{run.impact?.summary || '等待影响评估'}</h3></div>{run.impact && <span className={`risk risk-${run.impact.risk}`}>{run.impact.risk}</span>}</header>
      {run.impact && <dl><div><dt>维护窗口</dt><dd>{run.impact.requires_downtime ? '需要停机窗口' : '未标记停机'}</dd></div><div><dt>写入围栏</dt><dd>{run.impact.requires_write_fence ? '需要' : '不需要'}</dd></div><div><dt>预计时长</dt><dd>{formatDuration(run.impact.estimated_duration)}</dd></div></dl>}
    </section>
    <Lifecycle run={run} resolved={resolved} />
  </section>
}

function SiteState({ title, current }: { title: string; current: CurrentSiteState }) {
  return <section className="xrocket-state"><header><span>Current</span><h3>{title}</h3></header><dl><StateValue label="Master" value={current.master} /><StateValue label="Slave" value={current.slave} /><StateValue label="VIP" value={current.vip} /><StateValue label="Prefix" value={current.prefix} /><StateValue label="Gateway" value={current.gateway} /><StateValue label="Version" value={current.version} /><StateValue label="Topology" value={current.topology} /></dl></section>
}

function TargetState({ target }: { target: TargetSiteState }) {
  return <section className="xrocket-state xrocket-state-target"><header><span>Target</span><h3>目标站点</h3></header><dl><StateValue label="Master" value={target.master} /><StateValue label="Slave" value={target.slave} /><StateValue label="VIP" value={target.vip} /><StateValue label="Prefix" value={target.prefix} /><StateValue label="Gateway" value={target.gateway} /></dl></section>
}

function StateValue({ label, value }: { label: string; value: string }) {
  return <div><dt>{label}</dt><dd>{value || '未解析'}</dd></div>
}

function FindingSummary({ findings = [] }: { findings?: OperationFinding[] }) {
  if (findings.length === 0) return null
  return <ul className="xrocket-findings">{findings.map((finding) => <li key={`${finding.code}:${finding.summary}`} className={`finding-${finding.severity}`}><strong>{finding.summary}</strong><span>{finding.detail || finding.code}</span></li>)}</ul>
}

function Lifecycle({ run, resolved }: { run: OperationRun; resolved: boolean }) {
  const confirmedStates = new Set(['queued', 'acquiring_lock', 'creating_restore_point', 'running', 'verifying', 'succeeded', 'failed', 'rolling_back', 'rolled_back', 'rollback_failed', 'interrupted'])
  const execution = run.execution
  const stages = [
    { label: 'Precheck', detail: run.precheck ? run.precheck.passed ? '通过' : '阻断' : '等待', done: Boolean(run.precheck?.passed), failed: Boolean(run.precheck && !run.precheck.passed) },
    { label: 'Confirm', detail: confirmedStates.has(run.status.state) ? '已确认' : run.status.state === 'awaiting_confirmation' && resolved ? '等待确认' : '不可用', done: confirmedStates.has(run.status.state), failed: run.status.state === 'awaiting_confirmation' && !resolved },
    { label: 'Apply', detail: execution?.apply ? execution.apply.changed ? '已变更' : '无变更' : '未记录', done: Boolean(execution?.apply), failed: false },
    { label: 'Verify', detail: execution?.verification ? execution.verification.passed ? '通过' : '未通过' : '未记录', done: Boolean(execution?.verification?.passed), failed: execution?.verification?.passed === false },
    { label: 'Rollback', detail: execution?.rollback ? execution.rollback.restored ? '已恢复' : '未恢复' : '未记录', done: Boolean(execution?.rollback?.restored), failed: execution?.rollback?.restored === false },
    { label: 'VerifyRollback', detail: execution?.rollback_verification ? execution.rollback_verification.passed ? '通过' : '未通过' : '未记录', done: Boolean(execution?.rollback_verification?.passed), failed: execution?.rollback_verification?.passed === false },
  ]
  return <section className="xrocket-lifecycle"><header><div><span>Execution progress</span><h3>执行、验证与恢复</h3></div><code>{run.status.checkpoint}</code></header><ol>{stages.map((stage) => <li key={stage.label} className={stage.failed ? 'failed' : stage.done ? 'done' : ''}><span>{stage.done ? <Check size={15} /> : stage.failed ? <CircleAlert size={15} /> : <Clock3 size={15} />}</span><strong>{stage.label}</strong><small>{stage.detail}</small></li>)}</ol><div className="xrocket-final"><RotateCcw size={18} /><div><span>Final result</span><strong>{finalResult(run)}</strong></div><OperationStateBadge state={run.status.state} /></div></section>
}

function currentSiteState(run: OperationRun): CurrentSiteState {
  const value = asRecord(run.discovery?.snapshot.payload)
  const topologyValue = value.topology
  const topology = typeof topologyValue === 'string' ? topologyValue : topologyValue && typeof topologyValue === 'object' ? JSON.stringify(topologyValue) : stringValue(value.node_role)
  return {
    version: stringValue(value.version),
    topology,
    master: stringValue(value.current_master_address),
    slave: stringValue(value.current_slave_address),
    vip: stringValue(value.current_vip_address),
    prefix: numberOrString(value.prefix_length),
    gateway: stringValue(value.gateway_address),
    unresolved: booleanValue(value.version_unresolved) || booleanValue(value.topology_unresolved) || booleanValue(value.vip_unresolved)
      || !stringValue(value.version) || !topology || !stringValue(value.current_master_address) || !stringValue(value.current_slave_address) || !stringValue(value.current_vip_address),
  }
}

function targetSiteState(run: OperationRun): TargetSiteState {
  const parameters = asRecord(run.spec.parameters)
  return {
    master: stringValue(parameters.master_target_address),
    slave: stringValue(parameters.slave_target_address),
    vip: stringValue(parameters.vip_target_address),
    prefix: numberOrString(parameters.prefix_length),
    gateway: stringValue(parameters.gateway_address),
  }
}

function asRecord(value: unknown): UnknownRecord {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as UnknownRecord : {}
}

function stringValue(value: unknown) { return typeof value === 'string' ? value.trim() : '' }
function numberOrString(value: unknown) { return typeof value === 'number' || typeof value === 'string' ? String(value) : '' }
function booleanValue(value: unknown) { return value === true }

function formatDuration(nanoseconds: number) {
  if (!Number.isFinite(nanoseconds) || nanoseconds <= 0) return '未提供'
  const minutes = Math.round(nanoseconds / 60_000_000_000)
  return minutes >= 1 ? `${minutes} 分钟` : '< 1 分钟'
}

function finalResult(run: OperationRun) {
  const labels: Partial<Record<OperationRun['status']['state'], string>> = {
    succeeded: '变更已通过验证', rolled_back: '已回滚并完成恢复验证', rollback_failed: '回滚失败，需要人工恢复',
    failed: '执行失败', interrupted: '结果不确定，需要人工核对', blocked: '已在实际变更前阻断', canceled_before_apply: '已在实际变更前取消',
  }
  return labels[run.status.state] || '尚未形成最终结果'
}
