// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from '../api/client'
import { itemFixture, nodeFixture, remediationOfferFixture, repairOperationRunFixture, runFixture } from '../test/fixtures'
import type { OperationBatchConfirmationResponse, OperationRun } from '../api/types'
import { pollDelay, RunDetailPage } from './RunDetailPage'

beforeEach(() => {
  window.localStorage.clear()
  vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
  vi.spyOn(api, 'operationBatchConfirmations').mockResolvedValue({ confirmations: [], limit: 50, offset: 0 })
})

afterEach(() => {
  cleanup()
  window.localStorage.clear()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

function batchCheckId() { return 'net.ipv4.conf.all.accept_redirects.persisted' }

function repairItem(name = 'ICMP Redirect 持久化检查', id = 'net.ipv4.conf.all.accept_redirects.persisted') {
  return {
    ...itemFixture('unsafe'),
    id,
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

async function generatePreview() {
  fireEvent.click(screen.getByRole('button', { name: '生成批量修复预览' }))
  await waitFor(() => expect(screen.getByText('已冻结一次批量预览。')).toBeTruthy())
}


function batchResponse(batchId: string, checkRunId: string, entries: Array<{ taskId: string; checkId: string; nodeId?: string; run: OperationRun; digest?: string }>): OperationBatchConfirmationResponse {
  return {
    receipt: {
      api_version: 'setpoint.io/v1', kind: 'OperationBatchConfirmation', batch_id: batchId, source_check_run_id: checkRunId,
      confirmation_fingerprint: `sha256:${'f'.repeat(64)}`, confirmation_idempotency_key: 'batch-confirm-key', accepted_at: '2026-09-02T08:00:00Z',
      members: entries.map((entry, ordinal) => ({ ordinal, identity: { task_id: entry.taskId, check_id: entry.checkId, node_id: entry.nodeId ?? 'node-1' }, run_id: entry.run.metadata.id, plan_digest: entry.digest ?? entry.run.plan_digest ?? '', state: 'confirmed', updated_at: '2026-09-02T08:00:00Z' })),
    },
    runs: entries.map((entry) => entry.run),
  }
}

function independentRun(id: string, state: Parameters<typeof repairOperationRunFixture>[0] = 'awaiting_confirmation') {
  const run = repairOperationRunFixture(state)
  return { ...run, metadata: { ...run.metadata, id } }
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

describe('bulk remediation product closure', () => {
  it('keeps Server remediation authority and includes manual-only findings as preview exclusions', async () => {
    const actionable = repairItem('检查 A')
    const manual = repairItem('检查 B', 'manual.check')
    const actionableOffer = remediationOfferFixture({ check_id: actionable.id })
    const manualOffer = remediationOfferFixture({
      check_id: manual.id,
      availability: 'manual_only',
      supports_automatic_fix: false,
      supports_rollback: false,
      operation_id: undefined,
      operation_parameters: undefined,
      block_reason: 'no approved automatic repair capability matches this result',
    })
    vi.spyOn(api, 'run').mockResolvedValue(runFixture('completed', [actionable, manual], [actionableOffer, manualOffer]))
    const create = vi.spyOn(api, 'createOperationRun').mockResolvedValue(independentRun('repair-a'))

    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    await screen.findByText('检查 A')
    fireEvent.click(screen.getByRole('checkbox', { name: '选择修复 检查 A' }))
    fireEvent.click(screen.getByRole('checkbox', { name: '选择修复 检查 B' }))
    fireEvent.click(screen.getByRole('button', { name: '修复工作区 2' }))

    expect(screen.getByText('1 可修复 · 1 排除')).toBeTruthy()
    expect(screen.getAllByText('no approved automatic repair capability matches this result').length).toBeGreaterThanOrEqual(1)
    fireEvent.click(screen.getByRole('button', { name: '生成批量修复预览' }))
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1))
  })

  it('creates independent child OperationRuns, freezes one consolidated preview, and exposes one explicit confirmation', async () => {
    const first = repairItem('检查 A')
    const second = repairItem('检查 B', 'net.ipv4.conf.default.accept_redirects.persisted')
    const firstOffer = remediationOfferFixture({ check_id: first.id, operation_parameters: { check_id: first.id, target_value: 'runtime=0; persisted=0' } })
    const secondOffer = remediationOfferFixture({ check_id: second.id, operation_parameters: { check_id: second.id, target_value: 'runtime=0; persisted=0' } })
    vi.spyOn(api, 'run').mockResolvedValue(runFixture('completed', [first, second], [firstOffer, secondOffer]))
    const create = vi.spyOn(api, 'createOperationRun')
      .mockResolvedValueOnce(independentRun('repair-a'))
      .mockResolvedValueOnce(independentRun('repair-b'))
    const confirmedA = independentRun('repair-a', 'creating_restore_point')
    const confirmedB = independentRun('repair-b', 'creating_restore_point')
    const confirm = vi.spyOn(api, 'confirmOperationBatch').mockImplementation(async (batchId, checkRunId) => batchResponse(batchId, checkRunId, [
      { taskId: 'task-1', checkId: first.id, run: confirmedA },
      { taskId: 'task-1', checkId: second.id, run: confirmedB },
    ]))

    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    await screen.findByText('检查 A')
    fireEvent.click(screen.getByRole('button', { name: '选择当前可修复项' }))
    fireEvent.click(screen.getByRole('button', { name: '修复工作区 2' }))
    await generatePreview()

    expect(create).toHaveBeenCalledTimes(2)
    expect(screen.queryAllByText('分阶段迁移一个表')).toHaveLength(0)
    expect(screen.getAllByText(/将已验证持久化为 0/).length).toBeGreaterThanOrEqual(2)
    expect(screen.queryByRole('button', { name: '确认修复计划' })).toBeNull()
    const confirmAll = screen.getByRole('button', { name: 'CONFIRM ALL SELECTED REPAIR PLANS' })
    fireEvent.click(confirmAll)
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1))
    expect(confirm.mock.calls[0][3]).toEqual([
      expect.objectContaining({ check_id: first.id, run_id: 'repair-a', plan_digest: `sha256:${'b'.repeat(64)}` }),
      expect.objectContaining({ check_id: second.id, run_id: 'repair-b', plan_digest: `sha256:${'b'.repeat(64)}` }),
    ])
  })

  it('fails a stale child closed with the frozen digest and never silently adopts the changed plan', async () => {
    vi.spyOn(api, 'createOperationRun').mockResolvedValue(independentRun('repair-a'))
    const changed = independentRun('repair-a')
    changed.plan_digest = `sha256:${'c'.repeat(64)}`
    changed.plan = { ...changed.plan!, summary: 'CHANGED PLAN MUST NOT BE ADOPTED' }
    const operationRun = vi.spyOn(api, 'operationRun').mockResolvedValue(changed)
    const confirm = vi.spyOn(api, 'confirmOperationBatch').mockRejectedValue(new APIError('operation batch confirmation membership is stale or invalid', 409, 'operation_batch_stale_membership'))

    await openRepairWorkspace()
    await generatePreview()
    fireEvent.click(screen.getByRole('button', { name: '关闭' }))
    fireEvent.click(screen.getByRole('button', { name: '修复工作区 1' }))
    await waitFor(() => expect(operationRun).toHaveBeenCalledWith('repair-a'))
    expect(screen.getByText(/预览后计划已变化/)).toBeTruthy()
    expect(screen.getByText('CHANGED PLAN MUST NOT BE ADOPTED')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'CONFIRM ALL SELECTED REPAIR PLANS' }))
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1))
    expect(confirm.mock.calls[0][3]).toEqual([expect.objectContaining({ run_id: 'repair-a', plan_digest: `sha256:${'b'.repeat(64)}` })])
    expect(screen.getByText(/operation_batch_stale_membership/)).toBeTruthy()
  })

  it('keeps partial results truthful instead of reporting all selected repairs as succeeded', async () => {
    const first = repairItem('检查 A')
    const second = repairItem('检查 B', 'check-b')
    const third = repairItem('检查 C', 'check-c')
    const offers = [first, second, third].map((item) => remediationOfferFixture({ check_id: item.id, operation_parameters: { check_id: item.id, target_value: 'runtime=0; persisted=0' } }))
    vi.spyOn(api, 'run').mockResolvedValue(runFixture('completed', [first, second, third], offers))
    vi.spyOn(api, 'createOperationRun')
      .mockResolvedValueOnce(independentRun('repair-a'))
      .mockResolvedValueOnce(independentRun('repair-b'))
      .mockResolvedValueOnce(independentRun('repair-c'))
    vi.spyOn(api, 'confirmOperationBatch').mockImplementation(async (batchId, checkRunId) => batchResponse(batchId, checkRunId, [
      { taskId: 'task-1', checkId: first.id, run: independentRun('repair-a', 'succeeded') },
      { taskId: 'task-1', checkId: second.id, run: independentRun('repair-b', 'rolled_back') },
      { taskId: 'task-1', checkId: third.id, run: independentRun('repair-c', 'rollback_failed') },
    ]))

    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    await screen.findByText('检查 A')
    fireEvent.click(screen.getByRole('button', { name: '选择当前可修复项' }))
    fireEvent.click(screen.getByRole('button', { name: '修复工作区 3' }))
    await generatePreview()
    fireEvent.click(screen.getByRole('button', { name: 'CONFIRM ALL SELECTED REPAIR PLANS' }))

    await waitFor(() => expect(screen.getByText(/3 selected · 1 succeeded · 1 rolled back · 1 failed/)).toBeTruthy())
    expect(screen.queryByText('全部 3 个 child 修复成功')).toBeNull()
  })

  it('reuses persisted create identity after close/reopen and restores child truth instead of creating another run', async () => {
    const create = vi.spyOn(api, 'createOperationRun').mockResolvedValue(independentRun('repair-a'))
    vi.spyOn(api, 'operationRun').mockResolvedValue(independentRun('repair-a'))
    await openRepairWorkspace()
    await generatePreview()
    expect(create).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: '关闭' }))
    fireEvent.click(screen.getByRole('button', { name: '修复工作区 1' }))
    await waitFor(() => expect(api.operationRun).toHaveBeenCalledWith('repair-a'))
    expect(create).toHaveBeenCalledTimes(1)
  })

  it('reuses the persisted confirm idempotency key when confirmation must be retried', async () => {
    vi.spyOn(api, 'createOperationRun').mockResolvedValue(independentRun('repair-a'))
    const confirm = vi.spyOn(api, 'confirmOperationBatch')
      .mockRejectedValueOnce(new APIError('temporary unavailable', 503, 'server_failure'))
      .mockImplementationOnce(async (batchId, checkRunId) => batchResponse(batchId, checkRunId, [{ taskId: 'task-1', checkId: batchCheckId(), run: independentRun('repair-a', 'creating_restore_point') }]))
    await openRepairWorkspace()
    await generatePreview()
    const button = screen.getByRole('button', { name: 'CONFIRM ALL SELECTED REPAIR PLANS' })
    fireEvent.click(button)
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1))
    fireEvent.click(screen.getByRole('button', { name: 'CONFIRM ALL SELECTED REPAIR PLANS' }))
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(2))
    expect(confirm.mock.calls[0][2]).toBe(confirm.mock.calls[1][2])
  })

  it('cancels children independently and keeps the batch relationship/status truthful', async () => {
    vi.spyOn(api, 'createOperationRun').mockResolvedValue(independentRun('repair-a'))
    const canceled = independentRun('repair-a', 'canceled_before_apply')
    const cancel = vi.spyOn(api, 'cancelOperationRun').mockResolvedValue(canceled)
    await openRepairWorkspace()
    await generatePreview()
    fireEvent.click(screen.getByRole('button', { name: '取消剩余子操作' }))
    await waitFor(() => expect(cancel).toHaveBeenCalledWith('repair-a'))
    expect(screen.getByText(/1 selected · 0 succeeded · 0 rolled back · 0 failed · 1 canceled/)).toBeTruthy()
  })

  it('restores the persisted batch selection and child relation after a page remount', async () => {
    const create = vi.spyOn(api, 'createOperationRun').mockResolvedValue(independentRun('repair-a'))
    vi.spyOn(api, 'operationRun').mockResolvedValue(independentRun('repair-a', 'rolled_back'))
    const run = runFixture('completed', [repairItem()], [remediationOfferFixture()])
    vi.spyOn(api, 'run').mockResolvedValue(run)
    const view = render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    await screen.findByText('ICMP Redirect 持久化检查')
    fireEvent.click(screen.getByRole('button', { name: '选择当前可修复项' }))
    fireEvent.click(screen.getByRole('button', { name: '修复工作区 1' }))
    await generatePreview()
    view.unmount()

    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    await screen.findByText('ICMP Redirect 持久化检查')
    await waitFor(() => expect(screen.getByRole('button', { name: '修复工作区 1' })).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '修复工作区 1' }))
    await waitFor(() => expect(api.operationRun).toHaveBeenCalledWith('repair-a'))
    expect(create).toHaveBeenCalledTimes(1)
    expect((await screen.findAllByText('已安全恢复')).length).toBeGreaterThanOrEqual(1)
  })

  it('reconstructs an accepted batch from Server receipt with empty localStorage', async () => {
    const item = repairItem()
    const run = runFixture('completed', [item], [remediationOfferFixture()])
    vi.spyOn(api, 'run').mockResolvedValue(run)
    const child = independentRun('repair-server', 'rolled_back')
    vi.mocked(api.operationBatchConfirmations).mockResolvedValue({ confirmations: [batchResponse('batch-server', 'run-1', [{ taskId: 'task-1', checkId: item.id, run: child }])], limit: 50, offset: 0 })
    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    await screen.findByText('ICMP Redirect 持久化检查')
    await waitFor(() => expect(screen.getByRole('button', { name: '修复工作区 1' })).toBeTruthy())
    expect(window.localStorage.length).toBe(0)
    fireEvent.click(screen.getByRole('button', { name: '修复工作区 1' }))
    expect((await screen.findAllByText('已安全恢复')).length).toBeGreaterThanOrEqual(1)
  })
})

describe('pollDelay', () => {
  it('uses bounded exponential backoff', () => {
    expect([0, 1, 2, 3, 20].map(pollDelay)).toEqual([5_000, 10_000, 20_000, 30_000, 30_000])
  })
})
