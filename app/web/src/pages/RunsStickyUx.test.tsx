// @vitest-environment jsdom

import { cleanup, render, screen, within } from '@testing-library/react'
// @ts-expect-error Vitest runs this test under Node; the product tsconfig intentionally excludes Node types.
import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { checkFixture, nodeFixture } from '../test/fixtures'
import { RunsPage } from './RunsPage'

const finalUxCSS = readFileSync('src/final-ux.css', 'utf8')

afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe('PWV1 sticky Check toolbar', () => {
  it('renders one toolbar with all independent-Check controls and filters', async () => {
    const second = { ...checkFixture, id: 'db.high', name: '数据库高风险检查', category: 'Database', risk: 'high' as const }
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    vi.spyOn(api, 'checkDefinitions').mockResolvedValue({ definitions: [checkFixture, second] })
    vi.spyOn(api, 'checkBundles').mockResolvedValue({ bundles: [] })
    vi.spyOn(api, 'checkPolicies').mockResolvedValue({ policies: [] })
    vi.spyOn(api, 'runs').mockResolvedValue({ runs: [], limit: 50, offset: 0 })

    render(<RunsPage navigate={vi.fn()} />)

    const toolbars = await screen.findAllByTestId('check-picker-toolbar')
    expect(toolbars).toHaveLength(1)
    const toolbar = within(toolbars[0])
    expect(toolbar.getByRole('button', { name: '全选' })).toBeTruthy()
    expect(toolbar.getByRole('button', { name: '选择当前筛选' })).toBeTruthy()
    expect(toolbar.getByRole('button', { name: '清空' })).toBeTruthy()
    expect(toolbar.getByPlaceholderText('搜索检查项名称、ID 或说明')).toBeTruthy()
    expect(toolbar.getByLabelText('检查风险')).toBeTruthy()
    expect(toolbar.getByLabelText('检查系统')).toBeTruthy()
    expect(toolbar.getByLabelText('检查分类')).toBeTruthy()
    expect(toolbar.getByText('显示 2 / 2')).toBeTruthy()
  })

  it('contains reduced-motion handling for execution animation', () => {
    expect(finalUxCSS).toContain('@media (prefers-reduced-motion: reduce)')
    expect(finalUxCSS).toContain('animation: none')
    expect(finalUxCSS).toContain('.execution-activity-progress > span { transition: none; }')
  })
})
