// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from '../api/client'
import { siteFixture } from '../test/fixtures'
import { NodeBootstrapDialog } from './NodeBootstrapDialog'

const probe = {
  host_key_fingerprint: 'SHA256:host-key-test',
  os: 'linux',
  os_version: '22.03',
  arch: 'amd64',
  username: 'app',
  uid: 1039,
  mode: 'non-root' as const,
  home: '/home/app',
  agent_present: false,
  target_install_profile: {
    mode: 'non-root' as const,
    root: '/home/app/.local/share/setpoint/agent',
    binary_path: '/home/app/.local/share/setpoint/agent/bin/setpoint-agent',
    config_path: '/home/app/.local/share/setpoint/agent/config.json',
    identity_path: '/home/app/.local/share/setpoint/agent/state/agent-id',
    credential_path: '/home/app/.local/share/setpoint/agent/state/agent-credential.json',
    task_journal_path: '/home/app/.local/share/setpoint/agent/state/task-journal.json',
    enrollment_token_path: '/home/app/.local/share/setpoint/agent/bootstrap/enrollment-token',
  },
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  window.localStorage.clear()
  window.sessionStorage.clear()
})

describe('NodeBootstrapDialog', () => {
  it('requires explicit host-key confirmation and keeps the direct SSH password browser-memory only', async () => {
    const probeCall = vi.spyOn(api, 'probeNodeBootstrap').mockResolvedValue(probe)
    const apply = vi.spyOn(api, 'applyNodeBootstrap').mockResolvedValue({
      node_id: 'node-1', hostname: 'node-a', os: 'linux', os_version: '22.03', arch: 'amd64', agent_version: 'v1', status: 'online', site_id: siteFixture.id,
    })
    const completed = vi.fn()
    render(<NodeBootstrapDialog sites={[siteFixture]} close={vi.fn()} completed={completed} />)

    fireEvent.change(screen.getByLabelText('目标地址'), { target: { value: '192.0.2.20' } })
    fireEvent.change(screen.getByLabelText('SSH 用户'), { target: { value: 'app' } })
    fireEvent.change(screen.getByLabelText('SSH 密码'), { target: { value: 'SSH_PASSWORD_SENTINEL' } })
    fireEvent.change(screen.getByLabelText('所属站点（可选）'), { target: { value: siteFixture.id } })
    fireEvent.click(screen.getByRole('button', { name: '连接并探测' }))

    expect(await screen.findByText('SHA256:host-key-test')).toBeTruthy()
    expect(probeCall).toHaveBeenCalledWith('192.0.2.20', 22, 'app', 'SSH_PASSWORD_SENTINEL', undefined)
    expect((screen.getByRole('button', { name: '确认并部署 Agent' }) as HTMLButtonElement).disabled).toBe(true)
    expect(window.localStorage.length).toBe(0)
    expect(window.sessionStorage.length).toBe(0)
    expect(window.location.href).not.toContain('SSH_PASSWORD_SENTINEL')

    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: '确认并部署 Agent' }))

    await waitFor(() => expect(apply).toHaveBeenCalledWith(
      '192.0.2.20', 22, 'app', 'SSH_PASSWORD_SENTINEL', 'SHA256:host-key-test', siteFixture.id, undefined, undefined,
    ))
    await waitFor(() => expect(completed).toHaveBeenCalledWith(expect.objectContaining({ node_id: 'node-1', status: 'online' })))
  })

  it('sends gateway and target credentials only in request memory and confirms both host keys', async () => {
    const gatewayProbe = { ...probe, gateway_host_key_fingerprint: 'SHA256:gateway-key-test' }
    const probeCall = vi.spyOn(api, 'probeNodeBootstrap').mockResolvedValue(gatewayProbe)
    const apply = vi.spyOn(api, 'applyNodeBootstrap').mockResolvedValue({
      node_id: 'node-2', hostname: 'node-b', os: 'linux', os_version: '22.03', arch: 'amd64', agent_version: 'v1', status: 'online',
    })
    render(<NodeBootstrapDialog sites={[]} close={vi.fn()} completed={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('连接方式'), { target: { value: 'gateway' } })
    fireEvent.change(screen.getByLabelText('Gateway 地址'), { target: { value: '198.51.100.10' } })
    fireEvent.change(screen.getByLabelText('Gateway SSH 端口'), { target: { value: '5022' } })
    fireEvent.change(screen.getByLabelText('Gateway SSH 用户'), { target: { value: 'jump-user' } })
    fireEvent.change(screen.getByLabelText('Gateway SSH 密码'), { target: { value: 'GATEWAY_PASSWORD_SENTINEL' } })
    fireEvent.change(screen.getByLabelText('目标地址'), { target: { value: '10.0.0.20' } })
    fireEvent.change(screen.getByLabelText('SSH 用户'), { target: { value: 'target-user' } })
    fireEvent.change(screen.getByLabelText('SSH 密码'), { target: { value: 'TARGET_PASSWORD_SENTINEL' } })
    fireEvent.click(screen.getByRole('button', { name: '连接并探测' }))

    const gateway = {
      address: '198.51.100.10', port: 5022, username: 'jump-user', password: 'GATEWAY_PASSWORD_SENTINEL',
    }
    expect(await screen.findByText('SHA256:gateway-key-test')).toBeTruthy()
    expect(screen.getByText('SHA256:host-key-test')).toBeTruthy()
    expect(probeCall).toHaveBeenCalledWith('10.0.0.20', 22, 'target-user', 'TARGET_PASSWORD_SENTINEL', gateway)
    expect(window.localStorage.length).toBe(0)
    expect(window.sessionStorage.length).toBe(0)
    expect(window.location.href).not.toContain('GATEWAY_PASSWORD_SENTINEL')
    expect(window.location.href).not.toContain('TARGET_PASSWORD_SENTINEL')

    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: '确认并部署 Agent' }))

    await waitFor(() => expect(apply).toHaveBeenCalledWith(
      '10.0.0.20', 22, 'target-user', 'TARGET_PASSWORD_SENTINEL', 'SHA256:host-key-test', '', gateway, 'SHA256:gateway-key-test',
    ))
  })

  it('explains host-key changes as a pre-mutation stop', async () => {
    vi.spyOn(api, 'probeNodeBootstrap').mockResolvedValue(probe)
    vi.spyOn(api, 'applyNodeBootstrap').mockRejectedValue(new APIError('changed', 400, 'bootstrap_host_key_changed'))
    render(<NodeBootstrapDialog sites={[]} close={vi.fn()} completed={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('目标地址'), { target: { value: '192.0.2.20' } })
    fireEvent.change(screen.getByLabelText('SSH 用户'), { target: { value: 'app' } })
    fireEvent.change(screen.getByLabelText('SSH 密码'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: '连接并探测' }))
    await screen.findByText('SHA256:host-key-test')
    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: '确认并部署 Agent' }))

    expect(await screen.findByText(/任何远程变更前停止/)).toBeTruthy()
  })

  it('explains runtime reachability failure as a pre-token and pre-start stop', async () => {
    vi.spyOn(api, 'probeNodeBootstrap').mockResolvedValue(probe)
    vi.spyOn(api, 'applyNodeBootstrap').mockRejectedValue(new APIError('hidden transport detail', 502, 'bootstrap_agent_runtime_unreachable'))
    render(<NodeBootstrapDialog sites={[]} close={vi.fn()} completed={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('目标地址'), { target: { value: '192.0.2.20' } })
    fireEvent.change(screen.getByLabelText('SSH 用户'), { target: { value: 'app' } })
    fireEvent.change(screen.getByLabelText('SSH 密码'), { target: { value: 'secret' } })
    fireEvent.click(screen.getByRole('button', { name: '连接并探测' }))
    await screen.findByText('SHA256:host-key-test')
    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.click(screen.getByRole('button', { name: '确认并部署 Agent' }))

    expect(await screen.findByText(/未签发登记令牌，也未启动 Agent/)).toBeTruthy()
  })
})
