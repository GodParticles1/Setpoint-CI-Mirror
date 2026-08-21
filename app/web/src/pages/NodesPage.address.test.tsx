// @vitest-environment jsdom

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import { nodeFixture } from '../test/fixtures'
import { NodesPage } from './NodesPage'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('NodesPage observed source address', () => {
  it('labels the Server-observed address instead of presenting it as node identity', async () => {
    vi.spyOn(api, 'sites').mockResolvedValue({ sites: [] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })

    render(<NodesPage navigate={vi.fn()} />)

    const address = await screen.findByText('Server 观察源地址 192.0.2.10')
    expect(address.getAttribute('title')).toContain('不用于身份、路由或授权')
  })
})
