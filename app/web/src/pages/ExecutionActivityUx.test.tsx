// @vitest-environment jsdom

import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { nodeFixture, operationDefinitionFixture, operationRunFixture, runFixture } from '../test/fixtures'
import { OperationRunDetailPage } from './OperationRunDetailPage'
import { RunDetailPage } from './RunDetailPage'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe('PWV1 execution activity', () => {
  it('shows real completed/total task progress for a running Check Run', async () => {
    const run = runFixture('running')
    run.status.counts = { ...run.status.counts, total_tasks: 5, completed_tasks: 2, running_tasks: 1, pending_tasks: 2 }
    vi.spyOn(api, 'run').mockResolvedValue(run)
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })

    render(<RunDetailPage id={run.metadata.id} navigate={vi.fn()} />)

    const activity = await screen.findByRole('region', { name: '正在执行安全检查' })
    expect(within(activity).getByText('执行中')).toBeTruthy()
    expect(within(activity).getByText('真实任务进度：2 / 5')).toBeTruthy()
    const progress = screen.getByRole('progressbar', { name: '真实任务进度' })
    expect(progress.getAttribute('aria-valuenow')).toBe('2')
    expect(progress.getAttribute('aria-valuemax')).toBe('5')
  })

  it('does not present active Check execution UI on a terminal run', async () => {
    const run = runFixture('completed')
    vi.spyOn(api, 'run').mockResolvedValue(run)
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })

    render(<RunDetailPage id={run.metadata.id} navigate={vi.fn()} />)
    await screen.findByText('检查结果')
    expect(screen.queryByText('正在执行安全检查')).toBeNull()
    expect(screen.queryByRole('progressbar', { name: '真实任务进度' })).toBeNull()
  })

  it('renders real Operation lifecycle text without fake progress and stops on terminal state', async () => {
    const running = operationRunFixture('running')
    vi.spyOn(api, 'operationRun').mockResolvedValue(running)
    vi.spyOn(api, 'operation').mockResolvedValue(operationDefinitionFixture)
    const view = render(<OperationRunDetailPage id={running.metadata.id} navigate={vi.fn()} />)

    const activity = await screen.findByRole('region', { name: '受控操作进度' })
    expect(within(activity).getByText('正在执行受控变更')).toBeTruthy()
    expect(screen.queryByRole('progressbar')).toBeNull()

    view.unmount()
    vi.restoreAllMocks()
    const succeeded = operationRunFixture('succeeded')
    vi.spyOn(api, 'operationRun').mockResolvedValue(succeeded)
    vi.spyOn(api, 'operation').mockResolvedValue(operationDefinitionFixture)
    render(<OperationRunDetailPage id={succeeded.metadata.id} navigate={vi.fn()} />)
    await screen.findByText('示例在线迁移')
    expect(screen.queryByLabelText('受控操作进度')).toBeNull()
    expect(screen.queryByText('正在生成受控操作计划')).toBeNull()
    expect(screen.getByText('受控操作已完成')).toBeTruthy()
  })
})
