// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App, decodeRouteSegment } from './App'
import { api } from './api/client'

afterEach(() => {
  cleanup()
  window.history.replaceState({}, '', '/')
  vi.restoreAllMocks()
})

describe('routing', () => {
  it('rejects malformed percent encoding without crashing the app', () => {
    window.history.replaceState({}, '', '/runs/%E0%A4%A')
    render(<App />)
    expect(screen.getByText('检查批次地址无效')).toBeTruthy()
    expect(screen.getByRole('button', { name: '返回检查批次' })).toBeTruthy()
  })

  it('decodes valid route segments and rejects malformed ones', () => {
    expect(decodeRouteSegment('run%2D1')).toBe('run-1')
    expect(decodeRouteSegment('%E0%A4%A')).toBeNull()
  })

  it('rejects malformed operation run route segments', () => {
    window.history.replaceState({}, '', '/operations/runs/%E0%A4%A')
    render(<App />)
    expect(screen.getByText('操作记录地址无效')).toBeTruthy()
    expect(screen.getByRole('button', { name: '返回操作记录' })).toBeTruthy()
  })

  it('keeps Checks and Operations as separate navigation domains with one current page', async () => {
    vi.spyOn(api, 'checkDefinitions').mockResolvedValue({ definitions: [] })
    vi.spyOn(api, 'checkBundles').mockResolvedValue({ bundles: [] })
    vi.spyOn(api, 'checkPolicies').mockResolvedValue({ policies: [] })
    window.history.replaceState({}, '', '/checks')
    render(<App />)
    expect(await screen.findByRole('heading', { name: '检查项' })).toBeTruthy()
    expect(screen.getByRole('link', { name: '检查项' }).className).toContain('nav-active')
    expect(screen.getByRole('link', { name: '受控操作' }).className).not.toContain('nav-active')
    expect(screen.getAllByRole('link').filter((link) => link.getAttribute('aria-current') === 'page')).toHaveLength(1)
  })

  it('closes the mobile navigation when browser history changes', async () => {
    vi.spyOn(api, 'dashboard').mockResolvedValue({
      nodes_total: 0, nodes_online: 0, nodes_offline: 0, recent_runs: 0,
      safe: 0, unsafe: 0, manual_review: 0, error: 0, not_applicable: 0,
    })
    vi.spyOn(api, 'runs').mockResolvedValue({ runs: [], limit: 50, offset: 0 })
    vi.spyOn(api, 'sites').mockResolvedValue({ sites: [] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [] })
    render(<App />)

    fireEvent.click(screen.getByRole('button', { name: '打开导航' }))
    expect(screen.getAllByRole('button', { name: '关闭导航' })).toHaveLength(2)

    window.history.pushState({}, '', '/nodes')
    window.dispatchEvent(new PopStateEvent('popstate'))

    await waitFor(() => expect(screen.getByRole('button', { name: '打开导航' })).toBeTruthy())
    expect(screen.queryByRole('button', { name: '关闭导航' })).toBeNull()
    expect(screen.getAllByText('站点与节点').length).toBeGreaterThan(0)
  })
})
