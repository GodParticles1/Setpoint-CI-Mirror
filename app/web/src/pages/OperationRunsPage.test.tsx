// @vitest-environment jsdom

import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { operationRunFixture } from '../test/fixtures'
import { OperationRunsPage, operationPollDelay } from './OperationRunsPage'

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers() })

describe('OperationRunsPage', () => {
  it('backs off transient polling failures and keeps the last persisted list', async () => {
    vi.useFakeTimers()
    const runs = vi.spyOn(api, 'operationRuns')
      .mockResolvedValueOnce({ runs: [operationRunFixture('prechecking')], limit: 50, offset: 0 })
      .mockRejectedValueOnce(new Error('temporary'))
      .mockResolvedValueOnce({ runs: [operationRunFixture('awaiting_confirmation')], limit: 50, offset: 0 })
    render(<OperationRunsPage navigate={vi.fn()} />)
    await act(async () => { await Promise.resolve(); await Promise.resolve() })
    expect(screen.getByText('前置检查')).toBeTruthy()
    await act(async () => { await vi.advanceTimersByTimeAsync(5_000); await Promise.resolve() })
    expect(screen.getByText(/刷新失败/)).toBeTruthy()
    expect(screen.getByText('前置检查')).toBeTruthy()
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); await Promise.resolve() })
    expect(screen.getByText('等待确认')).toBeTruthy()
    expect(runs).toHaveBeenCalledTimes(3)
  })

  it('caps polling delay at thirty seconds', () => {
    expect(operationPollDelay(0)).toBe(5_000)
    expect(operationPollDelay(1)).toBe(10_000)
    expect(operationPollDelay(99)).toBe(30_000)
  })

  it('uses API offsets for lightweight pagination', async () => {
    const page = Array.from({ length: 50 }, (_, index) => ({ ...operationRunFixture('blocked'), metadata: { ...operationRunFixture().metadata, id: `run-${index}` } }))
    const runs = vi.spyOn(api, 'operationRuns').mockResolvedValueOnce({ runs: page, limit: 50, offset: 0 }).mockResolvedValueOnce({ runs: [], limit: 50, offset: 50 })
    render(<OperationRunsPage navigate={vi.fn()} />)
    expect(await screen.findByText('第 1 页')).toBeTruthy()
    screen.getByRole('button', { name: '下一页' }).click()
    expect(await screen.findByText('第 2 页')).toBeTruthy()
    expect(runs).toHaveBeenLastCalledWith(50, expect.any(AbortSignal))
  })
})
