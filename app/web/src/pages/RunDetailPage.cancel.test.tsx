// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { runFixture } from '../test/fixtures'
import { RunDetailPage } from './RunDetailPage'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('RunDetailPage cancellation', () => {
  it('shows a partial result when one task cannot be canceled', async () => {
    vi.spyOn(api, 'run').mockResolvedValue(runFixture('running'))
    const cancelRun = vi.spyOn(api, 'cancelRun').mockResolvedValue({
      run: runFixture('running'),
      cancel_report: {
        total_tasks: 2,
        canceled_tasks: 1,
        cancel_requested_tasks: 0,
        already_terminal_tasks: 0,
        failed_tasks: 1,
        results: [
          { task_id: 'task-1', outcome: 'canceled', phase: 'canceled' },
          { task_id: 'task-2', outcome: 'failed', phase: 'pending', error: { code: 'task_cancel_failed', message: 'cancellation request could not be recorded' } },
        ],
      },
    })

    render(<RunDetailPage id="run-1" navigate={vi.fn()} />)
    fireEvent.click(await screen.findByRole('button', { name: '取消批次' }))

    const report = await screen.findByRole('status')
    expect(report.textContent).toContain('批次已部分处理')
    expect(report.textContent).toContain('直接取消 1')
    expect(report.textContent).toContain('失败 1')
    expect(cancelRun).toHaveBeenCalledWith('run-1')
  })
})
