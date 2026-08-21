// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DeployPage } from './DeployPage'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('DeployPage onboarding guidance', () => {
  it('keeps manual deployment as the advanced Agent-only path', () => {
    render(<DeployPage />)

    expect(screen.getByText('高级：手动部署 Agent')).toBeTruthy()
    expect(screen.getByText(/普通节点优先从“站点与节点 → 添加节点”完成一次性 SSH Bootstrap/)).toBeTruthy()
    expect(screen.getByText(/Server → Task Transport → Agent/)).toBeTruthy()
    expect(screen.getByText(/不使用 SSH fallback/)).toBeTruthy()

    expect(screen.getByText(/SETPOINT_SERVER:8081/)).toBeTruthy()
    expect(screen.getByText(/identity_path/)).toBeTruthy()
    expect(screen.getByText(/credential_path/)).toBeTruthy()
    expect(screen.getByText(/task_journal_path/)).toBeTruthy()
    expect(screen.getByText(/Agent 会自行生成节点身份/)).toBeTruthy()
    expect(screen.getByText(/不需要手工填写 Node ID/)).toBeTruthy()
    expect(screen.queryByText(/"agent_id"/)).toBeNull()
  })

  it('shows an actionable error when the Clipboard API rejects', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
    })
    render(<DeployPage />)
    fireEvent.click(screen.getAllByRole('button', { name: '复制' })[0])
    expect((await screen.findByRole('alert')).textContent).toBe('复制失败，请手动选择文本')
  })
})
