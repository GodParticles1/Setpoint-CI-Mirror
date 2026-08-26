import { Building2, Pencil, Plus, RefreshCw, Save, Search, Server, ShieldCheck, Trash2, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { api, APIError } from '../api/client'
import type { Node, Site, TrustedExecutableRoot } from '../api/types'
import { Button, EmptyState, ErrorState, IconButton, Loading, PageHeader } from '../components/ui'
import { useResource } from '../hooks/useResource'
import { cleanTags, dateTime, shortID } from '../lib/format'
import { IdempotentOperation } from '../lib/idempotency'
import { NodeBootstrapDialog } from './NodeBootstrapDialog'

export function NodesPage({ navigate }: { navigate: (path: string) => void }) {
  const resource = useResource(async (signal) => {
    const [sites, nodes] = await Promise.all([api.sites(signal), api.nodes(signal)])
    return { sites: sites.sites, nodes: nodes.nodes }
  })
  const [siteEditor, setSiteEditor] = useState<Site | 'new' | null>(null)
  const [nodeEditor, setNodeEditor] = useState<Node | null>(null)
  const [bootstrapOpen, setBootstrapOpen] = useState(false)
  const [highlightNodeID, setHighlightNodeID] = useState('')
  const [query, setQuery] = useState('')
  const [status, setStatus] = useState<'all' | Node['status']>('all')
  const [siteFilter, setSiteFilter] = useState('all')

  if (resource.loading && !resource.data) return <Loading label="正在读取站点与节点" />
  if (resource.error && !resource.data) return <ErrorState message={resource.error} retry={resource.refresh} />
  const { sites, nodes } = resource.data!
  const normalizedQuery = query.trim().toLocaleLowerCase('zh-CN')
  const filteredNodes = nodes.filter((node) => {
    const matchesQuery = !normalizedQuery || [node.hostname, node.id, node.os, node.os_version, node.arch, node.site_name || '', ...node.tags]
      .some((value) => value.toLocaleLowerCase('zh-CN').includes(normalizedQuery))
    const matchesStatus = status === 'all' || node.status === status
    const matchesSite = siteFilter === 'all' || (siteFilter === 'unassigned' ? !node.site_id : node.site_id === siteFilter)
    return matchesQuery && matchesStatus && matchesSite
  })
  const filtersActive = Boolean(normalizedQuery || status !== 'all' || siteFilter !== 'all')

  return <>
    <PageHeader
      title="站点与节点"
      description={`${sites.length} 个站点 · ${nodes.length} 个节点`}
      actions={<>
        <IconButton label={resource.loading ? '正在刷新' : '刷新'} disabled={resource.loading} onClick={resource.refresh}><RefreshCw size={17} className={resource.loading ? 'spin' : ''} /></IconButton>
        <Button className="button-primary" onClick={() => setBootstrapOpen(true)}><Plus size={16} />添加节点</Button>
        <Button onClick={() => setSiteEditor('new')}><Building2 size={16} />新增站点</Button>
      </>}
    />
    {resource.error && <div className="notice-error" role="status">刷新失败，当前仍显示上一次成功数据：{resource.error}</div>}
    <section className="section-block section-first"><div className="section-heading"><div><h2>站点</h2><p>站点只用于对已经登记的节点进行轻量分组，不会创建服务器节点</p></div></div>
      {sites.length === 0 ? <EmptyState>尚未建立站点。添加节点时可以暂不分配站点，之后再按需要分组。</EmptyState> : <div className="table-wrap"><table><thead><tr><th>名称</th><th>节点</th><th>说明</th><th>更新时间</th><th /></tr></thead><tbody>
        {sites.map((site) => <tr key={site.id}><td><div className="cell-title"><Building2 size={16} /><strong>{site.name}</strong></div><small>{shortID(site.id)}</small></td><td>{site.node_count}</td><td>{site.description || '无说明'}</td><td>{dateTime(site.updated_at)}</td><td><IconButton label={`编辑站点 ${site.name}`} onClick={() => setSiteEditor(site)}><Pencil size={16} /></IconButton></td></tr>)}
      </tbody></table></div>}
    </section>
    <section className="section-block"><div className="section-heading"><div><h2>节点</h2><p>节点由目标 Linux 主机上的 Agent 完成安全登记后自动出现。Agent：{nodes.filter((node) => node.status === 'online').length} 在线 · {nodes.filter((node) => node.status === 'offline').length} 未在线</p></div><Button className="button-quiet" onClick={() => navigate('/deploy')}>高级：手动部署 Agent</Button></div>
      {nodes.length === 0 ? <EmptyState><div>尚无 Agent 登记节点。可使用“添加节点”完成一次性 SSH Bootstrap，或进入高级手工部署。</div><div className="empty-actions"><Button className="button-primary" onClick={() => setBootstrapOpen(true)}><Plus size={16} />添加节点</Button><Button onClick={() => navigate('/deploy')}><Server size={16} />手动部署 Agent</Button></div></EmptyState> : <>
        <div className="filter-bar" aria-label="节点筛选">
          <label className="search-field"><Search size={15} /><span className="sr-only">搜索节点</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索主机名、系统、标签或节点 ID" /></label>
          <label><span>Agent 状态</span><select aria-label="Agent 状态" value={status} onChange={(event) => setStatus(event.target.value as typeof status)}><option value="all">全部</option><option value="online">在线</option><option value="offline">未在线</option></select></label>
          <label><span>站点</span><select aria-label="节点站点" value={siteFilter} onChange={(event) => setSiteFilter(event.target.value)}><option value="all">全部</option><option value="unassigned">未分配</option>{sites.map((site) => <option key={site.id} value={site.id}>{site.name}</option>)}</select></label>
          <span className="filter-count">显示 {filteredNodes.length} / {nodes.length}</span>
        </div>
        {filteredNodes.length === 0 ? <EmptyState>{filtersActive ? '没有符合当前筛选条件的节点' : '尚无 Agent 登记节点'}</EmptyState> : <div className="table-wrap"><table><thead><tr><th>节点</th><th>Agent 状态</th><th>站点</th><th>系统</th><th>可信执行路径</th><th>标签</th><th>最近在线</th><th /></tr></thead><tbody>
          {filteredNodes.map((node) => <tr key={node.id} className={node.id === highlightNodeID ? 'node-highlight' : undefined}><td><div className="cell-title"><Server size={16} /><strong>{node.hostname}</strong></div><small title={node.id}>{shortID(node.id)}</small></td><td><span className={`node-state node-${node.status}`} title={node.status === 'online' ? 'Agent 当前在线' : 'Agent 当前未在线；不代表服务器故障'}>{node.status === 'online' ? 'Agent 在线' : 'Agent 未在线'}</span></td><td>{node.site_name || '未分配'}</td><td><strong>{node.os} {node.os_version}</strong><small>{node.arch} · Agent {node.agent_version}</small></td><td><TrustedRootStatus roots={node.trusted_executable_roots} /></td><td><div className="tag-list">{node.tags.length ? node.tags.map((tag) => <span key={tag}>{tag}</span>) : <span className="muted">无</span>}</div></td><td>{dateTime(node.last_seen_at)}<small title="仅为 Server 观察到的 TCP 连接源地址，不用于身份、路由或授权">{node.observed_source_address ? `Server 观察源地址 ${node.observed_source_address}` : ''}</small></td><td><IconButton label={`编辑节点 ${node.hostname}`} onClick={() => setNodeEditor(node)}><Pencil size={16} /></IconButton></td></tr>)}
        </tbody></table></div>}
      </>}
    </section>
    {bootstrapOpen && <NodeBootstrapDialog sites={sites} close={() => setBootstrapOpen(false)} completed={(result) => { setBootstrapOpen(false); setHighlightNodeID(result.node_id); resource.refresh() }} />}
    {siteEditor && <SiteDialog site={siteEditor} close={() => setSiteEditor(null)} saved={() => { setSiteEditor(null); resource.refresh() }} />}
    {nodeEditor && <NodeDialog node={nodeEditor} sites={sites} close={() => setNodeEditor(null)} saved={() => { setNodeEditor(null); resource.refresh() }} />}
  </>
}

function TrustedRootStatus({ roots }: { roots: TrustedExecutableRoot[] }) {
  if (roots.length === 0) return <span className="trusted-status trusted-none">未额外配置</span>
  const pending = roots.filter((root) => root.validation_status === 'pending_agent_validation').length
  return <div className="trusted-root-status"><span className={`trusted-status ${pending ? 'trusted-pending' : 'trusted-ready'}`}>{pending ? `${pending} 条待 Agent 校验` : '已配置'}</span><small>{roots.length} 条范围声明</small></div>
}

function SiteDialog({ site, close, saved }: { site: Site | 'new'; close: () => void; saved: () => void }) {
  const editing = site !== 'new'
  const [name, setName] = useState(editing ? site.name : '')
  const [description, setDescription] = useState(editing ? site.description : '')
  const [trustedRoots, setTrustedRoots] = useState(editing ? rootText(site.trusted_executable_roots, 'site') : '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const createOperation = useRef(new IdempotentOperation()).current
  const save = async () => {
    setBusy(true); setError('')
    try {
      const normalizedName = name.trim()
      const normalizedDescription = description.trim()
      const normalizedRoots = rootLines(trustedRoots)
      if (editing) await api.updateSite(site.id, normalizedName, normalizedDescription, normalizedRoots)
      else {
        const fingerprint = JSON.stringify({ name: normalizedName, description: normalizedDescription, trustedRoots: normalizedRoots })
        await api.createSite(normalizedName, normalizedDescription, normalizedRoots, createOperation.keyFor(fingerprint))
        createOperation.complete()
      }
      saved()
    }
    catch (reason) { setError(reason instanceof APIError ? reason.message : '保存失败') }
    finally { setBusy(false) }
  }
  const remove = async () => {
    if (!editing || !window.confirm(`删除站点“${site.name}”？`)) return
    setBusy(true); setError('')
    try { await api.deleteSite(site.id); saved() }
    catch (reason) { setError(reason instanceof APIError ? reason.message : '删除失败') }
    finally { setBusy(false) }
  }
  return <Dialog title={editing ? '编辑站点' : '新增站点'} close={close}><label className="field"><span>名称</span><input autoFocus maxLength={100} value={name} onChange={(event) => setName(event.target.value)} /></label><label className="field"><span>说明</span><textarea maxLength={1000} rows={4} value={description} onChange={(event) => setDescription(event.target.value)} /></label><TrustedRootsEditor label="站点可信可执行根" value={trustedRoots} onChange={setTrustedRoots} roots={editing ? site.trusted_executable_roots : []} />{error && <p className="inline-error">{error}</p>}<div className="dialog-actions">{editing && <Button className="button-danger" disabled={busy || site.node_count > 0} title={site.node_count > 0 ? '站点仍包含节点' : '删除站点'} onClick={remove}><Trash2 size={15} />删除</Button>}<span /><Button onClick={close}>取消</Button><Button className="button-primary" disabled={busy || !name.trim()} onClick={save}><Save size={15} />{busy ? '正在保存' : '保存'}</Button></div></Dialog>
}

function NodeDialog({ node, sites, close, saved }: { node: Node; sites: Site[]; close: () => void; saved: () => void }) {
  const [siteID, setSiteID] = useState(node.site_id || '')
  const [tags, setTags] = useState(node.tags.join(', '))
  const [notes, setNotes] = useState(node.notes)
  const [trustedRoots, setTrustedRoots] = useState(rootText(node.trusted_executable_roots, 'node'))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const save = async () => {
    setBusy(true); setError('')
    try { await api.updateNode(node.id, siteID, cleanTags(tags), notes.trim(), rootLines(trustedRoots)); saved() }
    catch (reason) { setError(reason instanceof APIError ? reason.message : '保存失败') }
    finally { setBusy(false) }
  }
  const remove = async () => {
    const warning = `删除节点“${node.hostname}”？\n\n删除会撤销该 Agent 在 Setpoint 中的登记和访问凭据，不会通过 SSH 卸载目标主机上的 Agent 进程。`
    if (!window.confirm(warning)) return
    setBusy(true); setError('')
    try { await api.deleteNode(node.id); saved() }
    catch (reason) { setError(reason instanceof APIError ? `${reason.code}: ${reason.message}` : '删除节点失败') }
    finally { setBusy(false) }
  }
  return <Dialog title={`编辑节点 · ${node.hostname}`} close={close}><label className="field"><span>站点</span><select value={siteID} onChange={(event) => setSiteID(event.target.value)}><option value="">未分配</option>{sites.map((site) => <option key={site.id} value={site.id}>{site.name}</option>)}</select></label><label className="field"><span>标签</span><input value={tags} onChange={(event) => setTags(event.target.value)} placeholder="多个标签使用英文逗号分隔" /></label><label className="field"><span>备注</span><textarea maxLength={1000} rows={5} value={notes} onChange={(event) => setNotes(event.target.value)} /></label><TrustedRootsEditor label="节点可信可执行根" value={trustedRoots} onChange={setTrustedRoots} roots={node.trusted_executable_roots} /><p className="risk-note">删除会撤销该 Agent 在 Setpoint 中的登记和访问凭据，不会通过 SSH 卸载目标主机上的 Agent 进程。</p>{error && <p className="inline-error">{error}</p>}<div className="dialog-actions"><Button className="button-danger" disabled={busy} onClick={remove}><Trash2 size={15} />删除节点</Button><span /><Button onClick={close}>取消</Button><Button className="button-primary" disabled={busy} onClick={save}><Save size={15} />{busy ? '正在保存' : '保存'}</Button></div></Dialog>
}

function TrustedRootsEditor({ label, value, onChange, roots }: { label: string; value: string; onChange: (value: string) => void; roots: TrustedExecutableRoot[] }) {
  return <details className="advanced-settings"><summary><ShieldCheck size={15} />可信可执行路径</summary><div className="advanced-settings-body"><p className="risk-note">新增目录会扩大 Agent 可执行边界。仅批准由管理员控制、不可被普通用户写入的固定目录；每次任务仍由 Agent 校验 realpath、所有权和权限。</p>{roots.length > 0 && <ul className="trusted-root-list">{roots.map((root) => <li key={`${root.scope}:${root.path}`}><code>{root.path}</code><span>{root.scope === 'site' ? '站点' : '节点'} · {root.validation_status === 'pending_agent_validation' ? '等待 Agent 校验' : root.validation_status}</span></li>)}</ul>}<label className="field"><span>{label}</span><textarea rows={4} value={value} onChange={(event) => onChange(event.target.value)} placeholder="每行一个绝对目录" /></label></div></details>
}

function rootLines(value: string): string[] {
  return Array.from(new Set(value.split(/\r?\n/).map((root) => root.trim()).filter(Boolean))).sort()
}

function rootText(roots: TrustedExecutableRoot[], scope: TrustedExecutableRoot['scope']): string {
  return roots.filter((root) => root.scope === scope).map((root) => root.path).sort().join('\n')
}

function Dialog({ title, close, children }: { title: string; close: () => void; children: React.ReactNode }) {
  useEffect(() => { const handler = (event: KeyboardEvent) => { if (event.key === 'Escape') close() }; window.addEventListener('keydown', handler); return () => window.removeEventListener('keydown', handler) }, [close])
  return <div className="dialog-layer" role="presentation"><button className="dialog-scrim" aria-label="关闭" onClick={close} /><section className="dialog" role="dialog" aria-modal="true" aria-label={title}><header><h2>{title}</h2><IconButton label="关闭" onClick={close}><X size={18} /></IconButton></header><div className="dialog-body">{children}</div></section></div>
}
