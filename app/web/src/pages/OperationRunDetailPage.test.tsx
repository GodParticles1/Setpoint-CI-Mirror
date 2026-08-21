// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from '../api/client'
import { operationDefinitionFixture, operationRunFixture } from '../test/fixtures'
import { OperationRunDetailPage } from './OperationRunDetailPage'

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers() })

describe('OperationRunDetailPage', () => {
  it('shows planning progress without confirmation while the task is active', async () => {
    const run = operationRunFixture('prechecking')
    run.plan = undefined; run.impact = undefined; run.plan_digest = undefined
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(operationDefinitionFixture)
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)
    expect(await screen.findByRole('heading', { name: '前置检查' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: '确认当前计划' })).toBeNull()
    expect(screen.getAllByText('尚无持久化结果')).toHaveLength(2)
    expect(screen.getByText('Discovery')).toBeTruthy()
    expect(screen.getByText('Precheck')).toBeTruthy()
    expect(screen.getByText('Confirmation')).toBeTruthy()
  })

  it('renders persisted planning evidence and no Apply or Rollback control', async () => {
    vi.spyOn(api, 'operationRun').mockResolvedValue(operationRunFixture())
    vi.spyOn(api, 'operation').mockResolvedValue(operationDefinitionFixture)
    render(<OperationRunDetailPage id="operation-run-1" navigate={vi.fn()} />)
    await screen.findByText('已冻结两个物理端点')
    expect(screen.getByText('前置检查通过')).toBeTruthy()
    expect(screen.getByText('分阶段迁移一个表')).toBeTruthy()
    expect(screen.getByText('目标端会写入数据')).toBeTruthy()
    expect(screen.getByText('product_apply_disabled')).toBeTruthy()
    expect(screen.getByText('计划与影响摘要已生成')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Apply|Rollback|执行变更|回滚/ })).toBeNull()
  })

  it('binds confirmation to the exact digest and persistently presents product_apply_disabled', async () => {
    const run = operationRunFixture()
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(operationDefinitionFixture)
    const confirm = vi.spyOn(api, 'confirmOperationRun').mockRejectedValue(new APIError('disabled', 409, 'product_apply_disabled'))
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)
    await screen.findByText('分阶段迁移一个表')
    fireEvent.click(screen.getByRole('button', { name: '确认当前计划' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog.textContent).toContain(run.plan_digest)
    expect(dialog.textContent).toContain('Product Apply 仍保持关闭')
    fireEvent.click(screen.getAllByRole('button', { name: '确认当前计划' })[1])
    await waitFor(() => expect(confirm).toHaveBeenCalledWith(run.metadata.id, run.plan_digest, expect.any(String)))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.getByText('product_apply_disabled')).toBeTruthy()
  })

  it('keeps the exact-digest dialog open on a digest conflict', async () => {
    const run = operationRunFixture()
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(operationDefinitionFixture)
    vi.spyOn(api, 'confirmOperationRun').mockRejectedValue(new APIError('计划摘要已变化', 409, 'operation_plan_digest_conflict'))
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)
    await screen.findByText('分阶段迁移一个表')
    fireEvent.click(screen.getByRole('button', { name: '确认当前计划' }))
    fireEvent.click(screen.getAllByRole('button', { name: '确认当前计划' })[1])
    expect(await screen.findByRole('alert')).toBeTruthy()
    expect(screen.getByRole('dialog')).toBeTruthy()
    expect(screen.getByRole('dialog').textContent).toContain(run.plan_digest)
  })

  it('shows blocked and recovery states as structured evidence without assigning the run-level block to a stage', async () => {
    const run = operationRunFixture('blocked')
    run.plan = undefined; run.impact = undefined; run.plan_digest = undefined
    run.status.block = { code: 'precheck_blocked', message: '目标不满足', safe_next_action: 'review_target', manual_review: true }
    run.status.recovery = { code: 'checkpoint_available', checkpoint: 'discovery_complete', safe_next_action: 'create_new_run', manual_review: false }
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(operationDefinitionFixture)
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)
    expect(await screen.findByText('目标不满足')).toBeTruthy()
    expect(screen.getByText('当前操作已停止在实际变更之前。')).toBeTruthy()
    expect(screen.getByText('checkpoint_available')).toBeTruthy()
    expect(screen.getAllByText('尚无持久化结果')).toHaveLength(2)
    expect(screen.getByLabelText('规划阶段').textContent).not.toContain('已阻断')
  })

  it('explains atomic-exchange safety blocking as stopped before mutation, not migration failure', async () => {
    const run = operationRunFixture('blocked')
    run.plan = undefined; run.impact = undefined; run.plan_digest = undefined
    run.status.block = {
      code: 'ATOMIC_EXCHANGE_NOT_AVAILABLE',
      message: 'atomic exchange capability is unavailable',
      safe_next_action: 'review_target_capability',
      manual_review: true,
    }
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(operationDefinitionFixture)

    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)

    expect(await screen.findByText('当前环境不满足该操作的安全执行条件，已停止在实际变更之前。')).toBeTruthy()
    expect(screen.getByText('ATOMIC_EXCHANGE_NOT_AVAILABLE')).toBeTruthy()
    expect(screen.getByText('review_target_capability')).toBeTruthy()
    expect(screen.queryByText('迁移失败')).toBeNull()
    expect(screen.queryByRole('button', { name: /Apply|Rollback|执行变更|回滚/ })).toBeNull()
  })

  it('cancels a pre-Apply run and updates the persisted state', async () => {
    const run = operationRunFixture('prechecking')
    run.plan = undefined; run.impact = undefined; run.plan_digest = undefined
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(operationDefinitionFixture)
    const canceled = { ...run, status: { ...run.status, state: 'canceled_before_apply' as const, checkpoint: 'canceled_before_apply' } }
    vi.spyOn(api, 'cancelOperationRun').mockResolvedValue(canceled)
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)
    await screen.findByText('前置检查通过')
    fireEvent.click(screen.getByRole('button', { name: '取消操作' }))
    expect(await screen.findByText('执行前已取消')).toBeTruthy()
    expect(screen.queryByRole('button', { name: '取消操作' })).toBeNull()
  })

  it('does not poll again after a canceled response', async () => {
    vi.useFakeTimers()
    const run = operationRunFixture('prechecking')
    run.plan = undefined; run.impact = undefined; run.plan_digest = undefined
    const load = vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(operationDefinitionFixture)
    const canceled = { ...run, status: { ...run.status, state: 'canceled_before_apply' as const, checkpoint: 'canceled_before_apply' } }
    vi.spyOn(api, 'cancelOperationRun').mockResolvedValue(canceled)
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)
    await vi.waitFor(() => expect(screen.getByText('前置检查通过')).toBeTruthy())
    fireEvent.click(screen.getByRole('button', { name: '取消操作' }))
    await vi.waitFor(() => expect(screen.getByText('执行前已取消')).toBeTruthy())
    const calls = load.mock.calls.length
    await vi.advanceTimersByTimeAsync(30_000)
    expect(load).toHaveBeenCalledTimes(calls)
    vi.useRealTimers()
  })
})
