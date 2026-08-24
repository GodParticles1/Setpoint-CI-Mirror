// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from '../api/client'
import { checkFixture, nodeFixture, runFixture } from '../test/fixtures'
import { RunsPage } from './RunsPage'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('RunsPage', () => {
  it('selects all online nodes and clears them without selecting offline nodes', async () => {
    const secondOnline = { ...nodeFixture, id: 'node-2', hostname: 'node-two' }
    const offline = { ...nodeFixture, id: 'node-3', hostname: 'node-three', status: 'offline' as const }
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture, secondOnline, offline] })
    vi.spyOn(api, 'checkDefinitions').mockResolvedValue({ definitions: [checkFixture] })
    vi.spyOn(api, 'checkBundles').mockResolvedValue({ bundles: [] })
    vi.spyOn(api, 'checkPolicies').mockResolvedValue({ policies: [] })
    vi.spyOn(api, 'runs').mockResolvedValue({ runs: [], limit: 50, offset: 0 })

    render(<RunsPage navigate={vi.fn()} />)
    fireEvent.click(await screen.findByRole('button', { name: '全选在线节点' }))
    expect(screen.getByText('2 已选')).toBeTruthy()
    expect(screen.getByText('node-one').closest('button')?.className).toContain('selected')
    expect(screen.getByText('node-two').closest('button')?.className).toContain('selected')
    expect(screen.getByText('node-three').closest('button')?.className).not.toContain('selected')

    const clearButtons = screen.getAllByRole('button', { name: '清空' })
    fireEvent.click(clearButtons[0])
    expect(screen.getAllByText('0 已选').length).toBeGreaterThan(0)
  })

  it('selects and clears all independent checks while preserving bundle and policy selection', async () => {
    const secondCheck = { ...checkFixture, id: 'test.second', name: '第二个测试检查' }
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    vi.spyOn(api, 'checkDefinitions').mockResolvedValue({ definitions: [checkFixture, secondCheck] })
    vi.spyOn(api, 'checkBundles').mockResolvedValue({ bundles: [{ id: 'bundle.test', name: '测试集合', description: 'bundle', category: 'Linux', check_ids: ['test.item'] }] })
    vi.spyOn(api, 'checkPolicies').mockResolvedValue({ policies: [{ id: 'policy.test', name: '测试策略', description: 'policy', bundle_ids: ['bundle.test'] }] })
    vi.spyOn(api, 'runs').mockResolvedValue({ runs: [], limit: 50, offset: 0 })
    const create = vi.spyOn(api, 'createRun').mockResolvedValue(runFixture())

    render(<RunsPage navigate={vi.fn()} />)
    fireEvent.click((await screen.findByText('node-one')).closest('button')!)
    fireEvent.click(screen.getByRole('button', { name: '集合 0' }))
    fireEvent.click(screen.getByText('测试集合').closest('button')!)
    fireEvent.click(screen.getByRole('button', { name: '策略 0' }))
    fireEvent.click(screen.getByText('测试策略').closest('button')!)
    fireEvent.click(screen.getByRole('button', { name: '检查项 0' }))
    fireEvent.click(screen.getByRole('button', { name: '全选' }))
    expect(screen.getByRole('button', { name: '检查项 2' })).toBeTruthy()
    fireEvent.click(screen.getAllByRole('button', { name: '清空' })[1])
    expect(screen.getByRole('button', { name: '检查项 0' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '集合 1' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '策略 1' })).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: '开始检查' }))
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1))
    expect(create.mock.calls[0][2]).toEqual({ checkIds: [], bundleIds: ['bundle.test'], policyIds: ['policy.test'] })
  })

  it('reuses the operation key after an ambiguous create failure', async () => {
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    vi.spyOn(api, 'checkDefinitions').mockResolvedValue({ definitions: [checkFixture] })
    vi.spyOn(api, 'checkBundles').mockResolvedValue({ bundles: [] })
    vi.spyOn(api, 'checkPolicies').mockResolvedValue({ policies: [] })
    vi.spyOn(api, 'runs').mockResolvedValue({ runs: [], limit: 50, offset: 0 })
    const create = vi.spyOn(api, 'createRun')
      .mockRejectedValueOnce(new APIError('请求已取消或超时', 0, 'request_aborted'))
      .mockResolvedValueOnce(runFixture())
    const navigate = vi.fn()

    render(<RunsPage navigate={navigate} />)
    fireEvent.click((await screen.findByText('node-one')).closest('button')!)
    fireEvent.click(screen.getByText('Linux 测试检查').closest('button')!)
    fireEvent.click(screen.getByRole('button', { name: '开始检查' }))
    expect(await screen.findByText('请求已取消或超时')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: '开始检查' }))
    await waitFor(() => expect(navigate).toHaveBeenCalledWith('/runs/run-1'))
    expect(create).toHaveBeenCalledTimes(2)
    expect(create.mock.calls[0][3]).toBe(create.mock.calls[1][3])
  })

  it('submits Bundle and Policy selections as independent request fields', async () => {
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    vi.spyOn(api, 'checkDefinitions').mockResolvedValue({ definitions: [checkFixture] })
    vi.spyOn(api, 'checkBundles').mockResolvedValue({ bundles: [{ id: 'bundle.test', name: '测试集合', description: 'bundle', category: 'Linux', check_ids: ['test.item'] }] })
    vi.spyOn(api, 'checkPolicies').mockResolvedValue({ policies: [{ id: 'policy.test', name: '测试策略', description: 'policy', bundle_ids: ['bundle.test'] }] })
    vi.spyOn(api, 'runs').mockResolvedValue({ runs: [], limit: 50, offset: 0 })
    const create = vi.spyOn(api, 'createRun').mockResolvedValue(runFixture())

    render(<RunsPage navigate={vi.fn()} />)
    fireEvent.click((await screen.findByText('node-one')).closest('button')!)
    fireEvent.click(screen.getByRole('button', { name: '集合 0' }))
    fireEvent.click(screen.getByText('测试集合').closest('button')!)
    fireEvent.click(screen.getByRole('button', { name: '策略 0' }))
    fireEvent.click(screen.getByText('测试策略').closest('button')!)
    fireEvent.click(screen.getByRole('button', { name: '开始检查' }))

    await waitFor(() => expect(create).toHaveBeenCalledTimes(1))
    expect(create.mock.calls[0][2]).toEqual({ checkIds: [], bundleIds: ['bundle.test'], policyIds: ['policy.test'] })
  })
})
