// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from '../api/client'
import { nodeFixture, siteFixture } from '../test/fixtures'
import { NodesPage } from './NodesPage'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('NodesPage site creation', () => {
  it('reuses the operation key when the create response is ambiguous', async () => {
    vi.spyOn(api, 'sites').mockResolvedValue({ sites: [] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [] })
    const create = vi.spyOn(api, 'createSite')
      .mockRejectedValueOnce(new APIError('网络请求失败', 0, 'network_error'))
      .mockResolvedValueOnce(siteFixture)

    render(<NodesPage navigate={vi.fn()} />)
    fireEvent.click(await screen.findByRole('button', { name: '新增站点' }))
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: '新站点' } })
    fireEvent.change(screen.getByLabelText('说明'), { target: { value: '说明' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByText('网络请求失败')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(create).toHaveBeenCalledTimes(2))
    expect(create.mock.calls[0][3]).toBe(create.mock.calls[1][3])
  })
})

describe('NodesPage node onboarding', () => {
  it('uses Add Node as the primary bootstrap path while preserving manual deployment', async () => {
    vi.spyOn(api, 'sites').mockResolvedValue({ sites: [] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [] })
    const navigate = vi.fn()

    render(<NodesPage navigate={navigate} />)

    expect(await screen.findByText(/一次性 SSH Bootstrap/)).toBeTruthy()
    fireEvent.click(screen.getAllByRole('button', { name: '添加节点' })[0])
    expect(screen.getByRole('dialog', { name: '添加节点' })).toBeTruthy()
    expect(navigate).not.toHaveBeenCalled()
  })
})

describe('NodesPage node removal', () => {
  it('shows the removal boundary, confirms, calls Server DELETE, closes, and refreshes nodes', async () => {
    vi.spyOn(api, 'sites').mockResolvedValue({ sites: [] })
    const nodes = vi.spyOn(api, 'nodes')
      .mockResolvedValueOnce({ nodes: [nodeFixture] })
      .mockResolvedValue({ nodes: [] })
    const remove = vi.spyOn(api, 'deleteNode').mockResolvedValue(undefined)
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)

    render(<NodesPage navigate={vi.fn()} />)
    fireEvent.click((await screen.findByRole('button', { name: /编辑节点/ })))

    expect(screen.getByText('删除会撤销该 Agent 在 Setpoint 中的登记和访问凭据，不会通过 SSH 卸载目标主机上的 Agent 进程。')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '删除节点' }))

    expect(confirm).toHaveBeenCalledTimes(1)
    expect(confirm.mock.calls[0][0]).toContain('不会通过 SSH 卸载目标主机上的 Agent 进程')
    await waitFor(() => expect(remove).toHaveBeenCalledWith(nodeFixture.id))
    await waitFor(() => expect(nodes.mock.calls.length).toBeGreaterThanOrEqual(2))
    await waitFor(() => expect(screen.queryByText('node-one')).toBeNull())
    expect(screen.queryByRole('dialog', { name: /编辑节点/ })).toBeNull()
  })

  it('shows the Server structured removal error without blocking an online node in React', async () => {
    vi.spyOn(api, 'sites').mockResolvedValue({ sites: [] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    const remove = vi.spyOn(api, 'deleteNode').mockRejectedValue(new APIError('node has active work', 409, 'active_work'))
    vi.spyOn(window, 'confirm').mockReturnValue(true)

    render(<NodesPage navigate={vi.fn()} />)
    fireEvent.click(await screen.findByRole('button', { name: /编辑节点/ }))
    const deleteButton = screen.getByRole('button', { name: '删除节点' }) as HTMLButtonElement
    expect(deleteButton.disabled).toBe(false)
    fireEvent.click(deleteButton)

    await waitFor(() => expect(remove).toHaveBeenCalledWith(nodeFixture.id))
    expect(await screen.findByText('active_work: node has active work')).toBeTruthy()
    expect(screen.getByRole('dialog', { name: /编辑节点/ })).toBeTruthy()
  })
})

describe('NodesPage trusted executable roots', () => {
  it('shows inherited scope and only submits explicit node roots', async () => {
    const site = {
      ...siteFixture,
      trusted_executable_roots: [{
        path: '/opt/company/site/bin', scope: 'site' as const, source: 'site:site-1', validation_status: 'pending_agent_validation' as const,
      }],
    }
    const node = {
      ...nodeFixture,
      site_id: site.id,
      site_name: site.name,
      trusted_executable_roots: [
        ...site.trusted_executable_roots,
        { path: '/opt/company/node/bin', scope: 'node' as const, source: 'node:node-1', validation_status: 'pending_agent_validation' as const },
      ],
    }
    vi.spyOn(api, 'sites').mockResolvedValue({ sites: [site] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [node] })
    const update = vi.spyOn(api, 'updateNode').mockResolvedValue(node)

    render(<NodesPage navigate={vi.fn()} />)
    expect(await screen.findByText('2 条待 Agent 校验')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /编辑节点/ }))
    fireEvent.click(screen.getByText('可信可执行路径'))
    expect(screen.getByText('/opt/company/site/bin')).toBeTruthy()
    expect(screen.getByText('站点 · 等待 Agent 校验')).toBeTruthy()
    const input = screen.getByLabelText('节点可信可执行根') as HTMLTextAreaElement
    expect(input.value).toBe('/opt/company/node/bin')
    fireEvent.change(input, { target: { value: '/opt/company/extra/bin\n/opt/company/node/bin' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => expect(update).toHaveBeenCalledWith(
      node.id, site.id, [], '', ['/opt/company/extra/bin', '/opt/company/node/bin'],
    ))
  })

  it('filters nodes locally by Agent status and does not imply host failure', async () => {
    const offlineNode = { ...nodeFixture, id: 'node-2', hostname: 'db-offline', status: 'offline' as const, site_id: siteFixture.id, site_name: siteFixture.name }
    vi.spyOn(api, 'sites').mockResolvedValue({ sites: [siteFixture] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture, offlineNode] })

    render(<NodesPage navigate={vi.fn()} />)
    await screen.findByText('node-one')
    fireEvent.change(screen.getByLabelText('Agent 状态'), { target: { value: 'offline' } })

    expect(screen.getByText('db-offline')).toBeTruthy()
    expect(screen.getByText('Agent 未在线').getAttribute('title')).toContain('不代表服务器故障')
    expect(screen.queryByText('node-one')).toBeNull()
    expect(screen.getByText('显示 1 / 2')).toBeTruthy()

    fireEvent.change(screen.getByPlaceholderText('搜索主机名、系统、标签或节点 ID'), { target: { value: 'missing' } })
    expect(screen.getByText('没有符合当前筛选条件的节点')).toBeTruthy()
  })
})
