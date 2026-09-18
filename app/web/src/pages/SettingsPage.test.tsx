// @vitest-environment jsdom

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SettingsPage } from './SettingsPage'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('SettingsPage Agent callback status', () => {
  it('shows automatic per-target route resolution and the runtime probe gate', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input)
      if (path === '/api/v1/settings') {
        return new Response(JSON.stringify({
          offline_after: '45s',
          minimum_refresh_interval: '2s',
          recommended_refresh_interval: '5s',
          maximum_run_tasks: 100,
        }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (path === '/api/v1/node-bootstrap/callback-status') {
        return new Response(JSON.stringify({
          mode: 'automatic_route',
          state: 'ready',
          agent_listen_address: '0.0.0.0:8081',
          runtime_probe_required: true,
        }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response(null, { status: 404 })
    }))

    render(<SettingsPage />)

    expect(await screen.findByText('自动（按目标路由）')).toBeTruthy()
    expect(screen.getByText(/每次 Add Node 按目标\/网关实际路由选择回调地址/)).toBeTruthy()
    expect(screen.getByText(/目标侧连通探测通过后才部署/)).toBeTruthy()
  })
})
