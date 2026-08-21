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
