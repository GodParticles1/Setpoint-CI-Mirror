// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from '../api/client'
import { operationDefinitionFixture, operationRunFixture, repairOperationRunFixture } from '../test/fixtures'
import { OperationRunDetailPage } from './OperationRunDetailPage'

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers() })

function definitionWithApply(apply: boolean) {
  const definition = structuredClone(operationDefinitionFixture)
  definition.availability.apply = apply
  definition.availability.block_code = apply ? '' : 'product_apply_disabled'
  return definition
}

function renderRun(state: Parameters<typeof operationRunFixture>[0], apply: boolean) {
  const run = operationRunFixture(state)
  vi.spyOn(api, 'operationRun').mockResolvedValue(run)
  vi.spyOn(api, 'operation').mockResolvedValue(definitionWithApply(apply))
  render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)
  return run
}

describe('OperationRunDetailPage execution surface', () => {
  it('keeps Apply=false confirmation wording planning-only and binds the exact digest', async () => {
    const run = operationRunFixture()
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(definitionWithApply(false))
    const confirm = vi.spyOn(api, 'confirmOperationRun').mockRejectedValue(new APIError('disabled', 409, 'product_apply_disabled'))
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)

    expect(await screen.findByText('实际变更执行尚未开放')).toBeTruthy()
    expect(within(screen.getByRole('status')).getByText(/确认后不会继续进入 Apply/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '确认当前计划' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog.textContent).toContain('当前不会继续进入实际 Apply')
    expect(dialog.textContent).toContain(run.plan_digest)
    fireEvent.click(within(dialog).getByRole('button', { name: '确认当前计划' }))
    await waitFor(() => expect(confirm).toHaveBeenCalledWith(run.metadata.id, run.plan_digest, expect.any(String)))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.getByText('product_apply_disabled')).toBeTruthy()
  })

  it('describes controlled execution for Apply=true without claiming Product Apply is disabled', async () => {
    const run = operationRunFixture()
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(definitionWithApply(true))
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)

    await screen.findByText('计划与影响摘要已生成')
    expect(screen.queryByText('实际变更执行尚未开放')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '确认当前计划' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog.textContent).toContain('Create Restore Point')
    expect(dialog.textContent).toContain('Apply')
    expect(dialog.textContent).toContain('Verify')
    expect(dialog.textContent).toContain('Rollback')
    expect(dialog.textContent).toContain('VerifyRollback')
    expect(dialog.textContent).not.toContain('Product Apply 仍保持关闭')
  })

  it.each(['queued', 'running', 'verifying', 'succeeded'] as const)('treats %s as past the confirmation gate', async (state) => {
    renderRun(state, true)
    const section = (await screen.findByRole('heading', { name: '计划确认' })).closest('section')!
    expect(within(section).getByText('已确认')).toBeTruthy()
    expect(section.textContent).not.toContain('等待中')
  })

  it('renders restore point, Apply, and Verify evidence only from persisted execution fields', async () => {
    const run = repairOperationRunFixture('succeeded')
    run.execution!.verification = { passed: true, summary: '运行时值已验证为 0' }
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(definitionWithApply(true))
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)

    const evidence = await screen.findByRole('region', { name: '执行证据' })
    expect(within(evidence).getByText('恢复点')).toBeTruthy()
    expect(within(evidence).getByText('restore-1')).toBeTruthy()
    expect(within(evidence).getByText('Apply')).toBeTruthy()
    expect(within(evidence).getByText('runtime_repaired')).toBeTruthy()
    expect(within(evidence).getByText('Verify')).toBeTruthy()
    expect(within(evidence).getByText('运行时值已验证为 0')).toBeTruthy()
  })

  it('renders rollback and rollback verification as distinct evidence', async () => {
    const run = repairOperationRunFixture('rolled_back')
    run.execution!.rollback_verification = { passed: true, summary: '原运行时值已恢复并验证' }
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(definitionWithApply(true))
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)

    const evidence = await screen.findByRole('region', { name: '执行证据' })
    expect(within(evidence).getByText('Rollback')).toBeTruthy()
    expect(within(evidence).getByText('runtime_restored')).toBeTruthy()
    expect(within(evidence).getByText('VerifyRollback')).toBeTruthy()
    expect(within(evidence).getByText('原运行时值已恢复并验证')).toBeTruthy()
    expect(screen.getByText('受控操作已完成回滚')).toBeTruthy()
  })

  it('keeps failed and rollback_failed terminal outcomes distinct', async () => {
    const failed = operationRunFixture('failed')
    vi.spyOn(api, 'operationRun').mockResolvedValue(failed)
    vi.spyOn(api, 'operation').mockResolvedValue(definitionWithApply(true))
    const view = render(<OperationRunDetailPage id={failed.metadata.id} navigate={vi.fn()} />)
    expect(await screen.findByText('受控操作已失败')).toBeTruthy()
    view.unmount()
    vi.restoreAllMocks()

    const rollbackFailed = repairOperationRunFixture('rollback_failed')
    vi.spyOn(api, 'operationRun').mockResolvedValue(rollbackFailed)
    vi.spyOn(api, 'operation').mockResolvedValue(definitionWithApply(true))
    render(<OperationRunDetailPage id={rollbackFailed.metadata.id} navigate={vi.fn()} />)
    expect(await screen.findByText('回滚未能完成，需要人工处理')).toBeTruthy()
  })
})
