// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from '../api/client'
import { itemFixture, nodeFixture, runFixture } from '../test/fixtures'
import { pollDelay, RunDetailPage } from './RunDetailPage'

beforeEach(() => {
  vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('RunDetailPage polling', () => {
  it('backs off after a transient failure, avoids overlap, recovers, and stops at terminal state', async () => {
    vi.useFakeTimers()
    let rejectRefresh: ((reason: unknown) => void) | undefined
    const run = vi.spyOn(api, 'run')
      .mockResolvedValueOnce(runFixture('running'))
      .mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectRefresh = reject }))
      .mockResolvedValueOnce(runFixture('completed'))

    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    await act(async () => { await Promise.resolve(); await Promise.resolve() })
    expect(screen.getByText('测试批次')).toBeTruthy()

    await act(async () => { await vi.advanceTimersByTimeAsync(5_000) })
    expect(run).toHaveBeenCalledTimes(2)
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
    expect(run).toHaveBeenCalledTimes(2)

    await act(async () => {
      rejectRefresh!(new APIError('服务暂不可用', 500, 'server_failure'))
      await Promise.resolve()
    })
    expect(screen.getByRole('status').textContent).toContain('10 秒后重试')
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(run).toHaveBeenCalledTimes(3)
    await act(async () => { await Promise.resolve() })
    expect(screen.queryByRole('status')).toBeNull()

    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(run).toHaveBeenCalledTimes(3)
  })

  it('aborts the active request when unmounted', () => {
    let signal: AbortSignal | undefined
    vi.spyOn(api, 'run').mockImplementation((_id, currentSignal) => {
      signal = currentSignal
      return new Promise(() => {})
    })
    const view = render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    expect(signal?.aborted).toBe(false)
    view.unmount()
    expect(signal?.aborted).toBe(true)
  })

  it('renders and filters all five result conclusions', async () => {
    const items = ['safe', 'unsafe', 'manual_review', 'error', 'not_applicable'].map((status) => itemFixture(status as Parameters<typeof itemFixture>[0]))
    vi.spyOn(api, 'run').mockResolvedValue(runFixture('completed', items))
    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    expect((await screen.findAllByText('人工复核')).length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('安全').length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('不安全').length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('检查错误').length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('不适用').length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('node-one').length).toBeGreaterThan(0)
    expect(screen.getByText('为什么需要人工复核')).toBeTruthy()
    expect(screen.getByText('需要核对现场策略')).toBeTruthy()
    expect(screen.getByText('检查未完成')).toBeTruthy()
    expect(screen.getByText('test failure')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: '人工复核' }))
    expect(screen.getByText('检查项 manual_review')).toBeTruthy()
    expect(screen.queryByText('检查项 safe')).toBeNull()
  })
})

describe('pollDelay', () => {
  it('uses bounded exponential backoff', () => {
    expect([0, 1, 2, 3, 20].map(pollDelay)).toEqual([5_000, 10_000, 20_000, 30_000, 30_000])
  })
})
