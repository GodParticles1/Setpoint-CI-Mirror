// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { runFixture } from '../test/fixtures'
import { DashboardPage } from './DashboardPage'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('DashboardPage', () => {
  it('prioritizes check errors, keeps node reachability visible and keeps Controlled Operations planning-only', async () => {
    vi.spyOn(api, 'dashboard').mockResolvedValue({
      nodes_total: 3,
      nodes_online: 2,
      nodes_offline: 1,
      recent_runs: 1,
      safe: 12,
      unsafe: 4,
      manual_review: 2,
      error: 3,
      not_applicable: 5,
      last_check_at: '2026-08-18T02:00:00Z',
    })
    const run = runFixture('partial_failed')
    run.status.counts.safe = 2
    run.status.counts.unsafe = 1
    run.status.counts.manual_review = 1
    run.status.counts.error = 1
    run.status.counts.not_applicable = 1
    vi.spyOn(api, 'runs').mockResolvedValue({ runs: [run], limit: 50, offset: 0 })
    const navigate = vi.fn()

    render(<DashboardPage navigate={navigate} />)

    expect(await screen.findByText('3 个检查错误需要处理')).toBeTruthy()
    expect(screen.getByText('在线节点')).toBeTruthy()
    expect(screen.getByText('未在线 Agent')).toBeTruthy()
    expect(screen.getByText('受控操作 · 仅规划')).toBeTruthy()
    expect(screen.getByText(/当前产品不会执行实际变更/)).toBeTruthy()
    expect(screen.getByLabelText('安全 2，不安全 1，人工复核 1，检查错误 1，不适用 1')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: /查看受控操作/ }))
    expect(navigate).toHaveBeenCalledWith('/operations')
  })

  it('uses a clear-state summary without inventing operation health data', async () => {
    vi.spyOn(api, 'dashboard').mockResolvedValue({
      nodes_total: 2,
      nodes_online: 2,
      nodes_offline: 0,
      recent_runs: 0,
      safe: 8,
      unsafe: 0,
      manual_review: 0,
      error: 0,
      not_applicable: 2,
    })
    vi.spyOn(api, 'runs').mockResolvedValue({ runs: [], limit: 50, offset: 0 })

    render(<DashboardPage navigate={vi.fn()} />)

    expect(await screen.findByText('当前没有需要优先处理的检查异常')).toBeTruthy()
    expect(screen.getByText('在线节点')).toBeTruthy()
    expect(screen.getByText('尚未创建检查批次')).toBeTruthy()
    expect(api.dashboard).toHaveBeenCalledTimes(1)
    expect(api.runs).toHaveBeenCalledTimes(1)
  })
})
