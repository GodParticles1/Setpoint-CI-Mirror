import { Copy, Download, Info, TerminalSquare } from 'lucide-react'
import { useState } from 'react'
import { Button, PageHeader } from '../components/ui'

const agentConfig = `{
  "server_url": "http://SETPOINT_SERVER:8081",
  "identity_path": "/var/lib/setpoint/agent-id",
  "credential_path": "/var/lib/setpoint/agent-credential.json",
  "task_journal_path": "/var/lib/setpoint/task-journal.json",
  "heartbeat_interval": "15s",
  "task_poll_interval": "2s",
  "command_timeout": "10s"
}`

const firstRun = `export SETPOINT_AGENT_ENROLLMENT_TOKEN='<一次性 Enrollment Token>'
./setpoint-agent -config /etc/setpoint/agent.json
unset SETPOINT_AGENT_ENROLLMENT_TOKEN`

export function DeployPage() {
  return <>
    <PageHeader title="高级：手动部署 Agent" description="普通节点优先从“站点与节点 → 添加节点”完成一次性 SSH Bootstrap；本页保留给需要人工控制交付过程的高级场景" />
    <section className="notice"><Info size={19} /><div><strong>这是高级手工入口</strong><p>手工部署不会改变运行期架构。Agent 完成安全登记后，后续检查和受控操作仍只通过 Server → Task Transport → Agent 执行，不使用 SSH fallback。</p></div></section>
    <section className="deploy-grid">
      <div className="deploy-step"><span>01</span><div><h2>准备 Agent</h2><p>选择与目标主机 CPU 架构匹配的 <code>setpoint-agent</code>，校验交付包摘要后放入受控目录。</p><div className="artifact-row"><Download size={18} /><div><strong>setpoint-agent-linux-amd64</strong><small>Linux · amd64</small></div></div><div className="artifact-row"><Download size={18} /><div><strong>setpoint-agent-linux-arm64</strong><small>Linux · arm64</small></div></div></div></div>
      <div className="deploy-step"><span>02</span><div><h2>确认 Agent Listener 可达</h2><p>Agent 连接的是 Server 的 <strong>Agent Listener</strong>，默认端口为 <code>8081</code>；管理页面仍只通过回环 Management Listener 访问。跨机器部署时，Server 的 Agent Listener 与 Agent Advertise URL 必须指向目标节点可达的可信地址。</p><CodeBlock value={agentConfig} /></div></div>
      <div className="deploy-step"><span>03</span><div><h2>首次安全登记</h2><p>在 Server 本机的管理入口创建短期 Enrollment Token，然后仅在目标节点首次启动 Agent 时提供。高级手工方式仍兼容一次性环境变量；自动“添加节点”流程使用 0600 临时 token file。Agent 会自行生成节点身份，不需要手工填写 Node ID。</p><CodeBlock value={firstRun} /></div></div>
      <div className="deploy-step"><span>04</span><div><h2>自动登记并上线</h2><p>首次登记成功后，Enrollment Token 会被换成节点本地 Agent Credential。Agent 随后持续心跳并领取任务，节点会自动出现在“站点与节点”页面。</p></div></div>
    </section>
    <section className="notice"><TerminalSquare size={19} /><div><strong>部署后检查</strong><p>节点显示“Agent 在线”后再发起只读检查。Agent 必须独立于 SSH 会话运行。当前未加密 HTTP 仅适用于受控可信网络，不应暴露到不可信网络。</p></div></section>
  </>
}

function CodeBlock({ value }: { value: string }) {
  const [copied, setCopied] = useState(false)
  const [copyError, setCopyError] = useState('')
  const copy = async () => {
    setCopyError('')
    try {
      if (!navigator.clipboard) throw new Error('Clipboard API unavailable')
      await navigator.clipboard.writeText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1200)
    } catch {
      setCopied(false)
      setCopyError('复制失败，请手动选择文本')
    }
  }
  return <div className="code-block"><pre>{value}</pre><Button className="button-quiet" onClick={copy}><Copy size={15} />{copied ? '已复制' : '复制'}</Button>{copyError && <p className="inline-error" role="alert">{copyError}</p>}</div>
}
