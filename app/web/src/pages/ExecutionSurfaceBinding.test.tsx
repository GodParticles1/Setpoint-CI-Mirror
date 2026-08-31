// @vitest-environment jsdom

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import { api } from '../api/client'
import { nodeFixture, operationDefinitionFixture } from '../test/fixtures'
import { OperationsPage } from './OperationsPage'

afterEach(() => {
  cleanup()
  window.history.replaceState({}, '', '/')
  vi.restoreAllMocks()
})

function operationWithApply(apply: boolean) {
  const operation = structuredClone(operationDefinitionFixture)
  operation.availability.apply = apply
  operation.availability.block_code = apply ? '' : 'product_apply_disabled'
  return operation
}

describe('Controlled Operations execution-surface binding', () => {
  it('keeps Apply=false wording scoped to the selected capability', async () => {
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operationWithApply(false)] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    render(<OperationsPage navigate={vi.fn()} />)

    expect(await screen.findByText('计划可用 · 实际执行未开放')).toBeTruthy()
    expect(screen.getByText(/该能力当前停止在规划与精确计划确认阶段/)).toBeTruthy()
    expect(screen.queryByText('受控操作当前仅开放规划能力')).toBeNull()
  })

  it('shows Apply=true capability without a global planning-only claim', async () => {
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operationWithApply(true)] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    render(<OperationsPage navigate={vi.fn()} />)

    expect(await screen.findByText('计划可用 · 实际执行可用')).toBeTruthy()
    expect(screen.getByText(/精确计划确认可允许 Server 继续进入受控执行/)).toBeTruthy()
    expect(screen.queryByText(/当前产品不会执行实际变更/)).toBeNull()
    expect(screen.queryByText(/仅生成规划证据/)).toBeNull()
    expect(screen.getByRole('button', { name: '生成操作计划' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /^Apply$|执行变更/ })).toBeNull()
  })

  it('keeps ordinary navigation capability-neutral and preserves the canonical Setpoint brand', async () => {
    vi.spyOn(api, 'dashboard').mockResolvedValue({ nodes_total: 0, nodes_online: 0, nodes_offline: 0, recent_runs: 0, safe: 0, unsafe: 0, manual_review: 0, error: 0, not_applicable: 0 })
    vi.spyOn(api, 'runs').mockResolvedValue({ runs: [], limit: 50, offset: 0 })
    render(<App />)

    await screen.findByRole('heading', { name: '受控操作' })
    expect(screen.queryByText('受控操作 · 仅规划')).toBeNull()
    expect(screen.queryByText('当前产品不会执行实际变更')).toBeNull()
    expect(screen.queryByText('受控操作规划')).toBeNull()
    const marks = document.querySelectorAll('img[src="/setpoint-mark.svg"]')
    expect(marks.length).toBeGreaterThanOrEqual(2)
  })
})
