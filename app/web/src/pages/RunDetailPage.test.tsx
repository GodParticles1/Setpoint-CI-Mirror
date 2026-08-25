// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from '../api/client'
import { itemFixture, nodeFixture, remediationOfferFixture, repairOperationRunFixture, runFixture } from '../test/fixtures'
import { pollDelay, RunDetailPage } from './RunDetailPage'

beforeEach(() => {
  vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

function repairItem(name = 'ICMP Redirect 持久化检查') {
  return {
    ...itemFixture('unsafe'),
    id: 'net.ipv4.conf.all.accept_redirects.persisted',
    name,
    current_value: 'runtime=1; persisted=0',
    recommended_value: 'runtime=0; persisted=0',
    supports_automatic_fix: true,
    supports_rollback: true,
  }
}

async function openRepairWorkspace(run = runFixture('completed', [repairItem()], [remediationOfferFixture()])) {
  vi.spyOn(api, 'run').mockResolvedValue(run)
  render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
  await screen.findByText('ICMP Redirect 持久化检查')
  fireEvent.click(screen.getByRole('button', { name: '选择当前可修复项' }))
  fireEvent.click(screen.getByRole('button', { name: '修复工作区 1' }))
}

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
})

describe('PWV1 remediation authority and execution', () => {
  it('renders all five conclusions and keeps existing filtering', async () => {
    const items = ['safe', 'unsafe', 'manual_review', 'error', 'not_applicable'].map((status) => itemFixture(status as Parameters<typeof itemFixture>[0]))
    vi.spyOn(api, 'run').mockResolvedValue(runFixture('completed', items))
    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    expect((await screen.findAllByText('人工复核')).length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('安全').length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('不安全').length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('检查错误').length).toBeGreaterThanOrEqual(1)
    expect(screen.getAllByText('不适用').length).toBeGreaterThanOrEqual(1)
    fireEvent.click(screen.getByRole('button', { name: '人工复核' }))
    expect(screen.getByText('检查项 manual_review')).toBeTruthy()
    expect(screen.queryByText('检查项 safe')).toBeNull()
  })

  it('uses the full remediation correlation and fails closed for an offer from another CheckRun', async () => {
    const item = repairItem()
    const wrongRunOffer = remediationOfferFixture({ check_run_id: 'run-other' })
    vi.spyOn(api, 'run').mockResolvedValue(runFixture('completed', [item], [wrongRunOffer]))
    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    await screen.findByText(item.name)
    expect((screen.getByRole('checkbox', { name: `选择修复 ${item.name}` }) as HTMLInputElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: '可自动修复' }))
    expect(screen.getByText('当前筛选没有结果')).toBeTruthy()
  })

  it('keeps legacy automatic-fix flags manual-only when the Server offer is manual_only', async () => {
    const item = repairItem()
    const manual = remediationOfferFixture({ availability: 'manual_only', supports_automatic_fix: false, supports_rollback: false, operation_id: undefined, operation_parameters: undefined, block_reason: 'no approved automatic repair capability matches this result' })
    vi.spyOn(api, 'run').mockResolvedValue(runFixture('completed', [item], [manual]))
    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    await screen.findByText(item.name)
    expect(screen.getByText('Server：仅人工')).toBeTruthy()
    expect(screen.getByText('no approved automatic repair capability matches this result')).toBeTruthy()
    expect((screen.getByRole('checkbox', { name: `选择修复 ${item.name}` }) as HTMLInputElement).disabled).toBe(true)
  })

  it('renders fixed target as locked and submits exact server-bound operation parameters', async () => {
    const create = vi.spyOn(api, 'createOperationRun').mockResolvedValue(repairOperationRunFixture('awaiting_confirmation'))
    await openRepairWorkspace()
    expect(screen.getByText('固定建议 / 不可编辑')).toBeTruthy()
    expect(screen.getByText(/允许值：runtime=0; persisted=0/)).toBeTruthy()
    expect(screen.queryByLabelText('目标值')).toBeNull()
    expect(screen.queryByRole('button', { name: '编辑后修复' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: '按建议值修复' }))
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1))
    expect(create.mock.calls[0][0]).toBe('linux.network.icmp_redirects.runtime_repair')
    expect(create.mock.calls[0][1]).toBe('node-1')
    expect(create.mock.calls[0][2]).toEqual([{ kind: 'node', node_id: 'node-1' }])
    expect(create.mock.calls[0][3]).toEqual({ check_id: 'net.ipv4.conf.all.accept_redirects.persisted', target_value: 'runtime=0; persisted=0' })
    expect(screen.getByText('真实计划预览')).toBeTruthy()
  })

  it('confirms the real plan digest with an idempotency key', async () => {
    vi.spyOn(api, 'createOperationRun').mockResolvedValue(repairOperationRunFixture('awaiting_confirmation'))
    const confirm = vi.spyOn(api, 'confirmOperationRun').mockResolvedValue(repairOperationRunFixture('creating_restore_point'))
    await openRepairWorkspace()
    fireEvent.click(screen.getByRole('button', { name: '按建议值修复' }))
    fireEvent.click(await screen.findByRole('button', { name: '确认修复计划' }))
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1))
    expect(confirm.mock.calls[0][0]).toBe('operation-run-1')
    expect(confirm.mock.calls[0][1]).toBe(`sha256:${'b'.repeat(64)}`)
    expect(confirm.mock.calls[0][2]).toBeTruthy()
    expect(screen.getByText('正在创建恢复点')).toBeTruthy()
  })

  it.each([
    ['creating_restore_point', '正在创建恢复点'],
    ['running', '正在修复'],
    ['verifying', '正在验证'],
    ['succeeded', '修复成功'],
    ['rolled_back', '已安全恢复'],
    ['interrupted', '修复结果不确定，需要人工确认'],
    ['rollback_failed', '自动回滚未能完成，需要人工恢复'],
  ] as const)('renders authoritative %s state without deciding a next action', async (state, label) => {
    vi.spyOn(api, 'createOperationRun').mockResolvedValue(repairOperationRunFixture(state))
    await openRepairWorkspace()
    fireEvent.click(screen.getByRole('button', { name: '按建议值修复' }))
    expect(await screen.findByText(label)).toBeTruthy()
    if (state === 'creating_restore_point') expect(screen.getByText(/恢复点：已创建并验证/)).toBeTruthy()
    if (state === 'running') expect(screen.getByText(/Apply：已执行变更/)).toBeTruthy()
    if (state === 'succeeded') expect(screen.getByText(/Verify：通过/)).toBeTruthy()
    if (state === 'rolled_back') {
      expect(screen.getByText(/Rollback：已恢复/)).toBeTruthy()
      expect(screen.getByText(/VerifyRollback：通过/)).toBeTruthy()
    }
    if (state === 'interrupted') expect(screen.getAllByText(/reconcile_before_retry_or_rollback/).length).toBeGreaterThanOrEqual(1)
    if (state === 'rollback_failed') expect(screen.getAllByText(/manual_recovery/).length).toBeGreaterThanOrEqual(1)
  })

  it('distinguishes rollback from verify-rollback using only Server checkpoint state', async () => {
    vi.spyOn(api, 'createOperationRun').mockResolvedValue(repairOperationRunFixture('rolling_back'))
    await openRepairWorkspace()
    fireEvent.click(screen.getByRole('button', { name: '按建议值修复' }))
    expect(await screen.findByText('验证未通过，正在自动回滚')).toBeTruthy()

    cleanup()
    vi.restoreAllMocks()
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    const verifyingRollback = repairOperationRunFixture('rolling_back')
    verifyingRollback.status.checkpoint = 'verify_rollback_queued'
    vi.spyOn(api, 'createOperationRun').mockResolvedValue(verifyingRollback)
    await openRepairWorkspace()
    fireEvent.click(screen.getByRole('button', { name: '按建议值修复' }))
    expect(await screen.findByText('正在验证回滚')).toBeTruthy()
  })

  it('shows validation/conflict error details and never fabricates success', async () => {
    vi.spyOn(api, 'createOperationRun').mockRejectedValue(new APIError('operation run state does not permit the requested transition', 409, 'operation_state_conflict'))
    await openRepairWorkspace()
    fireEvent.click(screen.getByRole('button', { name: '按建议值修复' }))
    expect(await screen.findByText(/operation_state_conflict/)).toBeTruthy()
    expect(screen.queryByText('修复成功')).toBeNull()
  })

  it('creates an independent OperationRun for every selected actionable offer', async () => {
    const first = repairItem('检查 A')
    const second = { ...repairItem('检查 B'), id: 'net.ipv4.conf.default.accept_redirects.persisted' }
    const firstOffer = remediationOfferFixture({ check_id: first.id, operation_parameters: { check_id: first.id, target_value: 'runtime=0; persisted=0' } })
    const secondOffer = remediationOfferFixture({ check_id: second.id, operation_parameters: { check_id: second.id, target_value: 'runtime=0; persisted=0' } })
    vi.spyOn(api, 'run').mockResolvedValue(runFixture('completed', [first, second], [firstOffer, secondOffer]))
    const base = repairOperationRunFixture('awaiting_confirmation')
    const create = vi.spyOn(api, 'createOperationRun')
      .mockResolvedValueOnce({ ...base, metadata: { ...base.metadata, id: 'repair-a' } })
      .mockResolvedValueOnce({ ...base, metadata: { ...base.metadata, id: 'repair-b' } })

    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    await screen.findByText('检查 A')
    fireEvent.click(screen.getByRole('button', { name: '选择当前可修复项' }))
    fireEvent.click(screen.getByRole('button', { name: '修复工作区 2' }))
    fireEvent.click(screen.getByRole('button', { name: '按建议值修复' }))
    await waitFor(() => expect(create).toHaveBeenCalledTimes(2))
    expect(create.mock.calls.map((call) => call[3])).toEqual(expect.arrayContaining([
      { check_id: first.id, target_value: 'runtime=0; persisted=0' },
      { check_id: second.id, target_value: 'runtime=0; persisted=0' },
    ]))
  })
})

describe('pollDelay', () => {
  it('uses bounded exponential backoff', () => {
    expect([0, 1, 2, 3, 20].map(pollDelay)).toEqual([5_000, 10_000, 20_000, 30_000, 30_000])
  })
})