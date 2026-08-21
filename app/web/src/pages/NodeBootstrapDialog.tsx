import { Check, KeyRound, LoaderCircle, Server, ShieldCheck, X } from 'lucide-react'
import { useState } from 'react'
import { api, APIError } from '../api/client'
import type { NodeBootstrapApplyResponse, NodeBootstrapGatewayInput, NodeBootstrapProbeResponse } from '../api/bootstrap-types'
import type { Site } from '../api/types'
import { Button, IconButton } from '../components/ui'

export function NodeBootstrapDialog({
  sites,
  close,
  completed,
}: {
  sites: Site[]
  close: () => void
  completed: (result: NodeBootstrapApplyResponse) => void
}) {
  const [connectionMode, setConnectionMode] = useState<'direct' | 'gateway'>('direct')
  const [address, setAddress] = useState('')
  const [port, setPort] = useState('22')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [gatewayAddress, setGatewayAddress] = useState('')
  const [gatewayPort, setGatewayPort] = useState('22')
  const [gatewayUsername, setGatewayUsername] = useState('')
  const [gatewayPassword, setGatewayPassword] = useState('')
  const [siteID, setSiteID] = useState('')
  const [probe, setProbe] = useState<NodeBootstrapProbeResponse | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState<'probe' | 'apply' | ''>('')
  const [error, setError] = useState('')

  const parsedPort = Number(port)
  const parsedGatewayPort = Number(gatewayPort)
  const targetReady = Boolean(address.trim() && username.trim() && password && Number.isInteger(parsedPort) && parsedPort > 0 && parsedPort <= 65535)
  const gatewayReady = connectionMode === 'direct' || Boolean(
    gatewayAddress.trim() && gatewayUsername.trim() && gatewayPassword
    && Number.isInteger(parsedGatewayPort) && parsedGatewayPort > 0 && parsedGatewayPort <= 65535,
  )
  const connectionReady = targetReady && gatewayReady

  const gatewayInput = (): NodeBootstrapGatewayInput | undefined => connectionMode === 'gateway'
    ? {
        address: gatewayAddress.trim(),
        port: parsedGatewayPort,
        username: gatewayUsername.trim(),
        password: gatewayPassword,
      }
    : undefined

  const runProbe = async () => {
    if (!connectionReady) return
    setBusy('probe')
    setError('')
    setProbe(null)
    setConfirmed(false)
    try {
      const result = await api.probeNodeBootstrap(address.trim(), parsedPort, username.trim(), password, gatewayInput())
      setProbe(result)
    } catch (reason) {
      setError(messageFor(reason))
    } finally {
      setBusy('')
    }
  }

  const runApply = async () => {
    if (!probe || !confirmed || probe.agent_present) return
    setBusy('apply')
    setError('')
    try {
      const result = await api.applyNodeBootstrap(
        address.trim(), parsedPort, username.trim(), password,
        probe.host_key_fingerprint, siteID, gatewayInput(), probe.gateway_host_key_fingerprint,
      )
      setPassword('')
      setGatewayPassword('')
      completed(result)
    } catch (reason) {
      setError(messageFor(reason))
    } finally {
      setBusy('')
    }
  }

  const deploymentStarted = busy === 'apply'
  return <div className="dialog-layer" role="presentation">
    <button className="dialog-scrim" aria-label="关闭" onClick={busy ? undefined : close} />
    <section className="dialog bootstrap-dialog" role="dialog" aria-modal="true" aria-label="添加节点">
      <header><h2>添加节点</h2><IconButton label="关闭" disabled={Boolean(busy)} onClick={close}><X size={18} /></IconButton></header>
      <div className="dialog-body">
        <ol className="bootstrap-steps" aria-label="添加节点进度">
          <Step index="1" label="连接" state={probe ? 'done' : busy === 'probe' ? 'active' : 'current'} />
          <Step index="2" label="确认身份" state={confirmed ? 'done' : probe ? 'current' : 'waiting'} />
          <Step index="3" label="部署" state={deploymentStarted ? 'active' : 'waiting'} />
          <Step index="4" label="登记" state={deploymentStarted ? 'active' : 'waiting'} />
          <Step index="5" label="在线" state={deploymentStarted ? 'active' : 'waiting'} />
        </ol>

        {!probe && <div className="bootstrap-form">
          <label className="field"><span>连接方式</span><select value={connectionMode} onChange={(event) => setConnectionMode(event.target.value as 'direct' | 'gateway')}><option value="direct">直连</option><option value="gateway">Gateway 跳板</option></select></label>
          {connectionMode === 'gateway' && <>
            <label className="field"><span>Gateway 地址</span><input autoFocus autoComplete="off" value={gatewayAddress} onChange={(event) => setGatewayAddress(event.target.value)} placeholder="跳板机 IP 或主机名" /></label>
            <label className="field"><span>Gateway SSH 端口</span><input inputMode="numeric" value={gatewayPort} onChange={(event) => setGatewayPort(event.target.value)} /></label>
            <label className="field"><span>Gateway SSH 用户</span><input autoComplete="off" value={gatewayUsername} onChange={(event) => setGatewayUsername(event.target.value)} /></label>
            <label className="field"><span>Gateway SSH 密码</span><input type="password" autoComplete="off" value={gatewayPassword} onChange={(event) => setGatewayPassword(event.target.value)} /></label>
          </>}
          <label className="field"><span>目标地址</span><input autoFocus={connectionMode === 'direct'} autoComplete="off" value={address} onChange={(event) => setAddress(event.target.value)} placeholder={connectionMode === 'gateway' ? 'Gateway 可访问的目标 IP 或主机名' : '目标 IP 或主机名'} /></label>
          <label className="field"><span>SSH 端口</span><input inputMode="numeric" value={port} onChange={(event) => setPort(event.target.value)} /></label>
          <label className="field"><span>SSH 用户</span><input autoComplete="off" value={username} onChange={(event) => setUsername(event.target.value)} /></label>
          <label className="field"><span>SSH 密码</span><input type="password" autoComplete="off" value={password} onChange={(event) => setPassword(event.target.value)} /></label>
          <label className="field"><span>所属站点（可选）</span><select value={siteID} onChange={(event) => setSiteID(event.target.value)}><option value="">暂不分配</option>{sites.map((site) => <option key={site.id} value={site.id}>{site.name}</option>)}</select></label>
          <div className="bootstrap-secret-note"><KeyRound size={16} /><p>{connectionMode === 'gateway' ? 'Gateway 与目标机 SSH 密码' : 'SSH 密码'}只用于本次首次 Agent 部署，不会保存为节点凭据，也不会用于后续检查或受控操作。</p></div>
        </div>}

        {probe && <div className="bootstrap-identity">
          {probe.gateway_host_key_fingerprint && <div className="bootstrap-fingerprint"><ShieldCheck size={20} /><div><span>Gateway SSH Host Key</span><code>{probe.gateway_host_key_fingerprint}</code></div></div>}
          <div className="bootstrap-fingerprint"><ShieldCheck size={20} /><div><span>目标 SSH Host Key</span><code>{probe.host_key_fingerprint}</code></div></div>
          <dl className="bootstrap-facts">
            {connectionMode === 'gateway' && <div><dt>Gateway</dt><dd>{gatewayAddress}:{parsedGatewayPort}</dd></div>}
            <div><dt>目标</dt><dd>{address}:{parsedPort}</dd></div>
            <div><dt>系统</dt><dd>{probe.os} {probe.os_version}</dd></div>
            <div><dt>架构</dt><dd>{probe.arch}</dd></div>
            <div><dt>部署身份</dt><dd>{probe.username} · uid {probe.uid} · {probe.mode}</dd></div>
          </dl>
          {probe.agent_present ? <div className="notice-info" role="status">目标已存在 Setpoint Agent。本流程不会覆盖或升级已有 Agent，请使用已有节点或高级手工流程确认现状。</div> : <label className="bootstrap-confirm"><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} /><span>我已核对并确认以上 SSH Host Key 指纹。只有确认后 Setpoint 才会上传并启动 Agent。</span></label>}
          {deploymentStarted && <div className="bootstrap-progress" role="status" aria-live="polite"><LoaderCircle className="spin" size={18} />正在部署 Agent、完成安全登记并等待节点在线。SSH 会话只用于这次 Bootstrap。</div>}
        </div>}

        {error && <p className="inline-error" role="alert">{error}</p>}
        <div className="dialog-actions bootstrap-actions">
          <span />
          {probe && !deploymentStarted ? <Button onClick={() => { setProbe(null); setConfirmed(false); setError('') }}>重新连接</Button> : <span />}
          <Button disabled={Boolean(busy)} onClick={close}>取消</Button>
          {!probe
            ? <Button className="button-primary" disabled={!connectionReady || Boolean(busy)} onClick={runProbe}>{busy === 'probe' ? <LoaderCircle className="spin" size={15} /> : <Server size={15} />}{busy === 'probe' ? '正在连接' : '连接并探测'}</Button>
            : <Button className="button-primary" disabled={!confirmed || probe.agent_present || Boolean(busy)} onClick={runApply}>{deploymentStarted ? <LoaderCircle className="spin" size={15} /> : <Check size={15} />}{deploymentStarted ? '正在部署' : '确认并部署 Agent'}</Button>}
        </div>
      </div>
    </section>
  </div>
}

function Step({ index, label, state }: { index: string; label: string; state: 'done' | 'active' | 'current' | 'waiting' }) {
  return <li className={`bootstrap-step bootstrap-step-${state}`} aria-current={state === 'current' ? 'step' : undefined}><span>{state === 'done' ? <Check size={13} /> : index}</span><strong>{label}</strong></li>
}

function messageFor(reason: unknown): string {
  if (!(reason instanceof APIError)) return '添加节点失败，请确认连接信息后重试'
  if (reason.code === 'bootstrap_host_key_changed') return 'SSH Host Key 已发生变化。Setpoint 已在任何远程变更前停止，请重新连接并核对新指纹。'
  if (reason.code === 'bootstrap_gateway_connect_failed') return '无法连接 Gateway SSH，请检查 Gateway 地址、端口和网络。'
  if (reason.code === 'bootstrap_gateway_auth_failed') return 'Gateway SSH 认证失败，请核对 Gateway 用户和密码。'
  if (reason.code === 'bootstrap_gateway_host_key_changed') return 'Gateway SSH Host Key 已变化。Setpoint 已在任何远程变更前停止，请重新连接并核对指纹。'
  if (reason.code === 'bootstrap_gateway_target_unreachable') return 'Gateway 无法连接目标 SSH，请检查目标地址、端口及 Gateway 到目标的网络。'
  if (reason.code === 'bootstrap_target_connect_failed') return '无法连接目标 SSH，请检查目标地址、端口和网络。'
  if (reason.code === 'bootstrap_target_auth_failed') return '目标 SSH 认证失败，请核对目标用户和密码。'
  if (reason.code === 'bootstrap_target_host_key_changed') return '目标 SSH Host Key 已变化。Setpoint 已在任何远程变更前停止，请重新连接并核对指纹。'
  if (reason.code === 'bootstrap_agent_already_present') return '目标已存在 Setpoint Agent，未执行覆盖或升级。'
  if (reason.code === 'bootstrap_unsupported_arch') return '当前目标架构没有可用的正式 Setpoint Agent。'
  if (reason.code === 'bootstrap_artifact_not_found') return 'Server 缺少该目标架构的正式 Agent 制品，未开始部署。'
  if (reason.code === 'bootstrap_artifact_hash_mismatch') return 'Agent 制品校验失败，未启动 Agent。'
  if (reason.code === 'bootstrap_agent_runtime_unreachable') return '目标节点无法访问 Server 的 Agent 地址。请检查 Agent Advertise URL 和目标侧路由；未签发登记令牌，也未启动 Agent。'
  if (reason.code === 'bootstrap_agent_start_failed') return 'Agent 启动失败，已停止部署并执行清理。'
  if (reason.code === 'bootstrap_enrollment_failed') return 'Agent 安全登记失败，已停止部署并执行清理。'
  if (reason.code === 'bootstrap_heartbeat_timeout') return 'Agent 未在限定时间内上线或保持心跳，请检查目标到 Server 的运行时连接。'
  if (reason.code === 'bootstrap_agent_advertise_url_unavailable') return 'Server 的 Agent Advertise URL 不可用于远程节点，未开始部署。'
  return reason.message
}
