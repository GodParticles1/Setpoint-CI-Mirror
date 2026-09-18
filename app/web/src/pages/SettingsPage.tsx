import { Clock3, Gauge, Network, RefreshCw, ShieldCheck } from 'lucide-react'
import { fetchAgentCallbackStatus } from '../api/bootstrap-status'
import { api } from '../api/client'
import { ErrorState, IconButton, Loading, PageHeader } from '../components/ui'
import { useResource } from '../hooks/useResource'

export function SettingsPage() {
  const settingsResource = useResource((signal) => api.settings(signal))
  const callbackResource = useResource((signal) => fetchAgentCallbackStatus(signal))

  if ((settingsResource.loading && !settingsResource.data) || (callbackResource.loading && !callbackResource.data)) return <Loading label="正在读取运行设置" />
  if ((settingsResource.error && !settingsResource.data) || (callbackResource.error && !callbackResource.data)) {
    return <ErrorState message={settingsResource.error || callbackResource.error} retry={() => { settingsResource.refresh(); callbackResource.refresh() }} />
  }
  const settings = settingsResource.data!
  const callback = callbackResource.data!
  const automatic = callback.mode === 'automatic_route'
  const callbackValue = callback.state === 'ready'
    ? (automatic ? '自动（按目标路由）' : '显式地址')
    : '不可用'
  const callbackDetail = automatic
    ? `Agent 监听 ${callback.agent_listen_address}；每次 Add Node 按目标/网关实际路由选择回调地址，目标侧连通探测通过后才部署。`
    : `Agent 监听 ${callback.agent_listen_address}；回调 ${callback.effective_advertise_url || '未配置'}，目标侧连通探测通过后才部署。`

  return <>
    <PageHeader title="设置" description="当前 Server 生效的只读运行边界" actions={<IconButton label="刷新" onClick={() => { settingsResource.refresh(); callbackResource.refresh() }}><RefreshCw size={17} /></IconButton>} />
    <section className="settings-list">
      <Setting icon={Clock3} label="节点离线阈值" value={settings.offline_after} />
      <Setting icon={RefreshCw} label="建议刷新间隔" value={settings.recommended_refresh_interval} detail={`最短 ${settings.minimum_refresh_interval}`} />
      <Setting icon={Gauge} label="单批次最大任务数" value={String(settings.maximum_run_tasks)} />
      <Setting icon={Network} label="Agent 回调" value={callbackValue} detail={callbackDetail} />
      <Setting icon={ShieldCheck} label="运行模式" value="只读检查" detail="未启用修改和受控变更" />
    </section>
  </>
}

function Setting({ icon: Icon, label, value, detail }: { icon: typeof Clock3; label: string; value: string; detail?: string }) {
  return <div className="setting-row"><span className="setting-icon"><Icon size={18} /></span><div><strong>{label}</strong>{detail && <small>{detail}</small>}</div><code>{value}</code></div>
}
