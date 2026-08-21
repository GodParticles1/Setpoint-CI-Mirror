// @vitest-environment jsdom

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import type { GranularCheckDefinition } from '../api/types'
import { checkFixture } from '../test/fixtures'
import { ChecksPage } from './ChecksPage'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('ChecksPage catalog contract regression', () => {
  it('does not crash when a historical payload contains null required arrays', async () => {
    const historical = {
      ...checkFixture,
      parameters: null,
      supported_systems: null,
      source_refs: null,
    } as unknown as GranularCheckDefinition

    vi.spyOn(api, 'checkDefinitions').mockResolvedValue({ definitions: [historical] })
    vi.spyOn(api, 'checkBundles').mockResolvedValue({ bundles: [] })
    vi.spyOn(api, 'checkPolicies').mockResolvedValue({ policies: [] })

    render(<ChecksPage navigate={vi.fn()} />)

    expect(await screen.findByText('Linux 测试检查')).toBeTruthy()
    expect(screen.getByText('独立检查')).toBeTruthy()
    expect(screen.getByText('内置规则')).toBeTruthy()
    expect(screen.queryByText(/参数说明/)).toBeNull()
  })

  it('renders empty and non-empty parameters, source refs and relationships together', async () => {
    vi.spyOn(api, 'checkDefinitions').mockResolvedValue({ definitions: [
      checkFixture,
      {
        ...checkFixture,
        id: 'test.parameterized',
        name: '参数化检查',
        parameters: [{ name: 'mode', type: 'string', description: '执行模式', required: false, options: ['safe', 'review'] }],
        source_refs: ['source:test.parameterized'],
      },
    ] })
    vi.spyOn(api, 'checkBundles').mockResolvedValue({ bundles: [{
      id: 'bundle.test', name: 'Linux 基础', description: 'bundle', category: 'Linux', check_ids: ['test.item', 'test.parameterized'],
    }] })
    vi.spyOn(api, 'checkPolicies').mockResolvedValue({ policies: [{
      id: 'policy.test', name: '主机策略', description: 'policy', bundle_ids: ['bundle.test'],
    }] })

    render(<ChecksPage navigate={vi.fn()} />)

    expect(await screen.findByText('2 个独立检查 · 1 个集合 · 1 个策略')).toBeTruthy()
    expect(screen.getByText('Linux 测试检查')).toBeTruthy()
    expect(screen.getByText('参数化检查')).toBeTruthy()
    expect(screen.getByText('参数说明 · 1')).toBeTruthy()
    expect(screen.getAllByText('集合 · Linux 基础')).toHaveLength(2)
    expect(screen.getAllByText('策略 · 主机策略')).toHaveLength(2)
    expect(screen.getAllByText('1 个来源')).toHaveLength(2)
  })
})
