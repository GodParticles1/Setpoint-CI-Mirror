// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { checkFixture } from '../test/fixtures'
import { ChecksPage } from './ChecksPage'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('ChecksPage', () => {
  it('renders Check, Bundle and Policy relationships without changing the execution contract', async () => {
    vi.spyOn(api, 'checkDefinitions').mockResolvedValue({ definitions: [{
      ...checkFixture,
      parameters: [{ name: 'mode', type: 'string', description: '测试模式', required: false, options: ['safe', 'review'] }],
    }] })
    vi.spyOn(api, 'checkBundles').mockResolvedValue({ bundles: [{ id: 'bundle.test', name: 'Linux 基础', description: 'bundle', category: 'Linux', check_ids: ['test.item'] }] })
    vi.spyOn(api, 'checkPolicies').mockResolvedValue({ policies: [{ id: 'policy.test', name: '主机策略', description: 'policy', bundle_ids: ['bundle.test'] }] })

    render(<ChecksPage navigate={vi.fn()} />)

    expect(await screen.findByText('1 个独立检查 · 1 个集合 · 1 个策略')).toBeTruthy()
    expect(screen.getByText('test.item')).toBeTruthy()
    expect(screen.getByText('集合 · Linux 基础')).toBeTruthy()
    expect(screen.getByText('策略 · 主机策略')).toBeTruthy()
    expect(screen.getByText('参数说明 · 1')).toBeTruthy()
    expect(screen.getByText('前端仅提供输入提示，Server 仍是最终参数契约权威。')).toBeTruthy()
  })

  it('filters the existing catalog entirely in the frontend', async () => {
    vi.spyOn(api, 'checkDefinitions').mockResolvedValue({ definitions: [
      checkFixture,
      { ...checkFixture, id: 'ssh.test', name: 'SSH 测试检查', category: 'SSH', risk: 'high', supported_systems: ['linux'] },
    ] })
    vi.spyOn(api, 'checkBundles').mockResolvedValue({ bundles: [] })
    vi.spyOn(api, 'checkPolicies').mockResolvedValue({ policies: [] })

    render(<ChecksPage navigate={vi.fn()} />)
    await screen.findByText('Linux 测试检查')
    fireEvent.change(screen.getByPlaceholderText('搜索名称、ID、分类、推荐值或 SourceRef'), { target: { value: 'ssh' } })

    expect(screen.getByText('SSH 测试检查')).toBeTruthy()
    expect(screen.queryByText('Linux 测试检查')).toBeNull()
    expect(screen.getByText('显示 1 / 2')).toBeTruthy()
  })
})
