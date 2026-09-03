// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import type { OperationDefinition } from '../api/types'
import { nodeFixture, operationRunFixture } from '../test/fixtures'
import { OperationsPage } from './OperationsPage'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

function clickHouseMigrationDefinition(): OperationDefinition {
  return {
    api_version: 'setpoint.io/v1',
    kind: 'OperationDefinition',
    metadata: {
      id: 'operation.clickhouse.online_migration',
      category: '数据迁移',
      name: 'ClickHouse 在线迁移',
      version: '0.2.0',
      description: '发现并验证源端与目标端，通过经过校验的暂存和 Atomic EXCHANGE 安全迁移选定的 ClickHouse 数据。',
      risk: 'high',
      impact: '仅按已确认的受控操作计划写入；有界 Apply 要求单节点 Atomic MergeTree 目标、Atomic EXCHANGE 能力和已验证的本次运行恢复点。',
      supported_systems: ['linux'],
      parameters: [
        { name: 'source', type: 'object', description: '源 ClickHouse', required: true, fields: [
          { name: 'host', type: 'string', description: '主机名或地址', required: true },
          { name: 'port', type: 'integer', description: 'Native 协议端口（常用 9000 / 9440）', required: false },
          { name: 'user', type: 'string', description: '数据库用户名', required: false },
          { name: 'secure', type: 'boolean', description: '使用安全 Native 协议', required: false },
        ] },
        { name: 'target', type: 'object', description: '目标 ClickHouse', required: true, fields: [
          { name: 'host', type: 'string', description: '主机名或地址', required: true },
          { name: 'port', type: 'integer', description: 'Native 协议端口（常用 9000 / 9440）', required: false },
          { name: 'user', type: 'string', description: '数据库用户名', required: false },
          { name: 'secure', type: 'boolean', description: '使用安全 Native 协议', required: false },
        ] },
        { name: 'database', type: 'string', description: '待迁移数据库', required: true },
        { name: 'tables', type: 'string[]', description: '待迁移表', required: true },
        { name: 'time_column', type: 'string', description: '事件时间列（可选）', required: false },
        { name: 'start_time', type: 'string', description: '时间范围开始（可选，RFC3339）', required: false },
        { name: 'end_time', type: 'string', description: '时间范围结束（可选，RFC3339）', required: false },
      ],
      secret_requirements: [
        { id: 'clickhouse_source_credential', description: '源 ClickHouse 运行时凭据引用（可选）', required: false },
        { id: 'clickhouse_target_credential', description: '目标 ClickHouse 运行时凭据引用（可选）', required: false },
      ],
    },
    capability_digest: `sha256:${'c'.repeat(64)}`,
    availability: { planning: true, apply: true, block_code: '', secret_delivery: false },
  }
}

it('submits the proven ClickHouse migration intent without exposing strategy selection', async () => {
  const definition = clickHouseMigrationDefinition()
  vi.spyOn(api, 'operations').mockResolvedValue({ operations: [definition] })
  vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
  const run = operationRunFixture()
  run.spec.operation_id = definition.metadata.id
  const create = vi.spyOn(api, 'createOperationRun').mockResolvedValue(run)
  const navigate = vi.fn()

  render(<OperationsPage navigate={navigate} />)

  expect(await screen.findByRole('heading', { name: 'ClickHouse 在线迁移' })).toBeTruthy()
  expect(screen.getByText('计划可用 · 实际执行可用')).toBeTruthy()
  expect(definition.metadata.parameters?.some((parameter) => parameter.name === 'strategy')).toBe(false)

  fireEvent.change(screen.getByLabelText('执行节点'), { target: { value: 'node-1' } })
  const hosts = screen.getAllByLabelText('主机名或地址 *')
  const ports = screen.getAllByLabelText('Native 协议端口（常用 9000 / 9440）')
  const users = screen.getAllByLabelText('数据库用户名')
  const secure = screen.getAllByLabelText('使用安全 Native 协议')
  fireEvent.change(hosts[0], { target: { value: 'source.internal' } })
  fireEvent.change(ports[0], { target: { value: '9000' } })
  fireEvent.change(users[0], { target: { value: 'reader' } })
  fireEvent.click(secure[0])
  fireEvent.change(hosts[1], { target: { value: 'target.internal' } })
  fireEvent.change(ports[1], { target: { value: '9000' } })
  fireEvent.change(users[1], { target: { value: 'writer' } })
  fireEvent.change(screen.getByLabelText('待迁移数据库 *'), { target: { value: 'events' } })
  fireEvent.change(screen.getByLabelText('待迁移表 *'), { target: { value: 'history, audit' } })

  fireEvent.click(screen.getByRole('button', { name: '生成操作计划' }))

  await waitFor(() => expect(create).toHaveBeenCalledTimes(1))
  expect(create).toHaveBeenCalledWith(
    'operation.clickhouse.online_migration',
    'node-1',
    [{ kind: 'node', node_id: 'node-1' }],
    {
      source: { host: 'source.internal', port: 9000, user: 'reader', secure: true },
      target: { host: 'target.internal', port: 9000, user: 'writer' },
      database: 'events',
      tables: ['history', 'audit'],
    },
    [],
    expect.any(String),
  )
  expect(navigate).toHaveBeenCalledWith('/operations/runs/operation-run-1')
})

it('keeps time-range inputs optional and visible under the advanced scope boundary', async () => {
  vi.spyOn(api, 'operations').mockResolvedValue({ operations: [clickHouseMigrationDefinition()] })
  vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })

  render(<OperationsPage navigate={vi.fn()} />)
  await screen.findByRole('heading', { name: 'ClickHouse 在线迁移' })
  fireEvent.click(screen.getByText('高级范围选项（可选）'))

  expect(screen.getByLabelText('事件时间列（可选）')).toBeTruthy()
  expect(screen.getByLabelText('时间范围开始（可选，RFC3339）')).toBeTruthy()
  expect(screen.getByLabelText('时间范围结束（可选，RFC3339）')).toBeTruthy()
})
