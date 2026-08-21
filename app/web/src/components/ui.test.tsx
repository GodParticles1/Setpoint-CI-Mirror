// @vitest-environment jsdom

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ErrorState, Loading } from './ui'

afterEach(cleanup)

describe('shared UI states', () => {
  it('announces loading as a polite busy status', () => {
    render(<Loading label="正在读取检查结果" />)
    const status = screen.getByRole('status')
    expect(status.getAttribute('aria-live')).toBe('polite')
    expect(status.getAttribute('aria-busy')).toBe('true')
    expect(status.textContent).toContain('正在读取检查结果')
  })

  it('announces API errors as alerts without changing retry behavior', () => {
    render(<ErrorState message="通信失败" retry={() => undefined} />)
    expect(screen.getByRole('alert').textContent).toContain('通信失败')
    expect(screen.getByRole('button', { name: '重试' })).toBeTruthy()
  })
})
