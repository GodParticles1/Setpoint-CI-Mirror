// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import type { OperationDefinition, OperationRun } from '../api/types'
import { nodeFixture, operationRunFixture } from '../test/fixtures'
import { OperationRunDetailPage } from './OperationRunDetailPage'
import { OperationsPage } from './OperationsPage'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

const definition: OperationDefinition = {
  api_version: 'setpoint.io/v1', kind: 'OperationDefinition', capability_digest: `sha256:${'c'.repeat(64)}`,
  metadata: {
    id: 'xrocket.site.readdress', category: 'xRocket 站点运维', name: 'xRocket 站点地址变更', version: '1.0.0',
    description: '规划 xRocket 双机站点的 IP 地址变更。', risk: 'critical', impact: '可能导致管理连接和服务中断。', supported_systems: ['xrocket'],
    parameters: [
      { name: 'master_target_address', type: 'string', description: 'Master 节点目标 IP 地址', required: true },
      { name: 'slave_target_address', type: 'string', description: 'Slave 节点目标 IP 地址', required: true },
      { name: 'vip_target_address', type: 'string', description: 'Virtual IP (VIP) 目标地址', required: true },
      { name: 'prefix_length', type: 'integer', description: '子网前缀长度', required: false },
      { name: 'gateway_address', type: 'string', description: '网关地址', required: false },
    ],
  },
  availability: { planning: true, apply: false, block_code: 'product_apply_disabled', secret_delivery: false },
}

function xrocketRun(resolved: boolean): OperationRun {
  const run = operationRunFixture('awaiting_confirmation')
  run.spec = {
    ...run.spec,
    operation_id: definition.metadata.id,
    parameters: {
      master_target_address: '192.168.20.10', slave_target_address: '192.168.20.11', vip_target_address: '192.168.20.100',
      prefix_length: 24, gateway_address: '192.168.20.1',
    },
  }
  const current = resolved ? {
    version: 'C90', node_role: 'master / slave', current_master_address: '10.10.0.10', current_slave_address: '10.10.0.11',
    current_vip_address: '10.10.0.100', prefix_length: 24, gateway_address: '10.10.0.1', version_unresolved: false, topology_unresolved: false,
  } : { version_unresolved: true, topology_unresolved: true, discovery_error: 'physical discovery unavailable' }
  run.discovery = {
    applicable: resolved, summary: resolved ? 'current site discovered' : 'discovery blocked', targets: run.spec.targets,
    snapshot: { schema_version: 'xrocket.readdress.discovery.v1', payload: current },
    findings: resolved ? [] : [{ code: 'TOPOLOGY_UNRESOLVED', severity: 'blocking', summary: '站点拓扑未解析' }],
  }
  run.precheck = {
    passed: resolved, summary: resolved ? 'xRocket readdress precheck passed' : 'xRocket readdress precheck blocked',
    snapshot: { schema_version: 'xrocket.readdress.discovery.v1', payload: current },
    findings: resolved ? [] : [{ code: 'VERSION_UNRESOLVED', severity: 'blocking', summary: '版本未解析' }],
  }
  run.impact = {
    summary: '地址变化需要维护窗口', risk: 'critical', requires_downtime: true, requires_write_fence: true,
    estimated_duration: 1_800_000_000_000, estimated_data_change_bytes: 0,
    changes: resolved ? [
      { target: { kind: 'component', component: 'xrocket', resource: 'master_node' }, before: '10.10.0.10', after: '192.168.20.10', risk: 'high' },
      { target: { kind: 'component', component: 'xrocket', resource: 'vip' }, before: '10.10.0.100', after: '192.168.20.100', risk: 'high' },
    ] : [],
  }
  return run
}

describe('xRocket Site Readdress composer', () => {
  it('submits the five shared parameter names unchanged', async () => {
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [definition] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    const create = vi.spyOn(api, 'createOperationRun').mockResolvedValue(xrocketRun(false))
    render(<OperationsPage navigate={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: '站点当前状态' })).toBeTruthy()
    expect(screen.getAllByText('待发现')).toHaveLength(5)
    fireEvent.change(screen.getByLabelText('执行节点'), { target: { value: 'node-1' } })
    fireEvent.change(screen.getByLabelText('Master 节点目标 IP 地址 *'), { target: { value: '192.168.20.10' } })
    fireEvent.change(screen.getByLabelText('Slave 节点目标 IP 地址 *'), { target: { value: '192.168.20.11' } })
    fireEvent.change(screen.getByLabelText('Virtual IP (VIP) 目标地址 *'), { target: { value: '192.168.20.100' } })
    fireEvent.change(screen.getByLabelText('子网前缀长度'), { target: { value: '24' } })
    fireEvent.change(screen.getByLabelText('网关地址'), { target: { value: '192.168.20.1' } })
    fireEvent.click(screen.getByRole('button', { name: '运行 Precheck 并生成计划' }))

    await waitFor(() => expect(create).toHaveBeenCalledTimes(1))
    expect(create.mock.calls[0][3]).toEqual({
      master_target_address: '192.168.20.10', slave_target_address: '192.168.20.11', vip_target_address: '192.168.20.100',
      prefix_length: 24, gateway_address: '192.168.20.1',
    })
  })
})

describe('xRocket Site Readdress run closure', () => {
  it('fails closed when discovery and precheck are unresolved', async () => {
    const run = xrocketRun(false)
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(definition)
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)

    expect(await screen.findByText('当前环境信息尚未解析，禁止进入实际变更')).toBeTruthy()
    expect(screen.queryByRole('button', { name: '确认当前计划' })).toBeNull()
    expect(screen.getAllByText('未解析').length).toBeGreaterThan(0)
    expect(screen.queryByText('10.10.0.10')).toBeNull()
  })

  it('shows only persisted current, target, diff, and rollback verification evidence', async () => {
    const run = xrocketRun(true)
    run.status = { ...run.status, state: 'rolled_back', checkpoint: 'rollback_verified' }
    run.execution = {
      apply: { changed: true, checkpoint: 'readdress_applied', state: { schema_version: 'v1', payload: {} } },
      verification: { passed: false, summary: 'new VIP probe failed' },
      rollback: { restored: true, checkpoint: 'addresses_restored', state: { schema_version: 'v1', payload: {} } },
      rollback_verification: { passed: true, summary: 'original site connectivity restored' },
    }
    vi.spyOn(api, 'operationRun').mockResolvedValue(run)
    vi.spyOn(api, 'operation').mockResolvedValue(definition)
    render(<OperationRunDetailPage id={run.metadata.id} navigate={vi.fn()} />)

    expect((await screen.findAllByText('10.10.0.10')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('192.168.20.10').length).toBeGreaterThan(0)
    expect(screen.getByText('2 项 Server 差异')).toBeTruthy()
    expect(screen.getAllByText('VerifyRollback').length).toBeGreaterThan(0)
    expect(screen.getByText('已回滚并完成恢复验证')).toBeTruthy()
  })
})
