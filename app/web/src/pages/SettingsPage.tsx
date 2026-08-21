import { Clock3, Gauge, RefreshCw, ShieldCheck } from 'lucide-react'
import { api } from '../api/client'
import { ErrorState, IconButton, Loading, PageHeader } from '../components/ui'
import { useResource } from '../hooks/useResource'

export function SettingsPage() {
  const resource = useResource((signal) => api.settings(signal))
  if (resource.loading && !resource.data) return <Loading label="正在读取运行设置" />
  if (resource.error && !resource.data) return <ErrorState message={resource.error} retry={resource.refresh} />
  const settings = resource.data!
  return <>
    <PageHeader title="设置" description="当前 Server 生效的只读运行边界" actions={<IconButton label="刷新" onClick={resource.refresh}><RefreshCw size={17} /></IconButton>} />
    <section className="settings-list">
      <Setting icon={Clock3} label="节点离线阈值" value={settings.offline_after} />
      <Setting icon={RefreshCw} label="建议刷新间隔" value={settings.recommended_refresh_interval} detail={`最短 ${settings.minimum_refresh_interval}`} />
      <Setting icon={Gauge} label="单批次最大任务数" value={String(settings.maximum_run_tasks)} />
      <Setting icon={ShieldCheck} label="运行模式" value="只读检查" detail="未启用修改和受控变更" />
    </section>
  </>
}

function Setting({ icon: Icon, label, value, detail }: { icon: typeof Clock3; label: string; value: string; detail?: string }) {
  return <div className="setting-row"><span className="setting-icon"><Icon size={18} /></span><div><strong>{label}</strong>{detail && <small>{detail}</small>}</div><code>{value}</code></div>
}
