// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../api/client'
import type { OperationDefinition } from '../api/types'
import { nodeFixture, operationDefinitionFixture, operationRunFixture } from '../test/fixtures'
import { OperationsPage } from './OperationsPage'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

function clickhouseOperation(): OperationDefinition {
  const operation = structuredClone(operationDefinitionFixture)
  operation.metadata = {
    ...operation.metadata,
    id: 'operation.clickhouse.online_migration',
    category: 'data_migration',
    name: 'ClickHouse online migration',
    version: '0.2.0',
    description: 'Discover, validate and migrate selected ClickHouse datasets through staged verified execution.',
    impact: 'Writes only through a verified Controlled Operation plan.',
    parameters: [
      { name: 'source', type: 'object', description: 'Source ClickHouse endpoint', required: true, fields: [
        { name: 'host', type: 'string', description: 'Host name or address', required: true },
        { name: 'port', type: 'integer', description: 'Native protocol port; defaults to 9000 or 9440', required: false },
        { name: 'user', type: 'string', description: 'Database user name', required: false },
        { name: 'secure', type: 'boolean', description: 'Use the secure native protocol', required: false },
      ] },
      { name: 'target', type: 'object', description: 'Target ClickHouse endpoint', required: true, fields: [
        { name: 'host', type: 'string', description: 'Host name or address', required: true },
        { name: 'port', type: 'integer', description: 'Native protocol port; defaults to 9000 or 9440', required: false },
        { name: 'user', type: 'string', description: 'Database user name', required: false },
        { name: 'secure', type: 'boolean', description: 'Use the secure native protocol', required: false },
      ] },
      { name: 'database', type: 'string', description: 'Database to migrate', required: true },
      { name: 'tables', type: 'string[]', description: 'Selected tables', required: true },
      { name: 'time_column', type: 'string', description: 'Optional event-time column for a bounded migration', required: false },
      { name: 'start_time', type: 'string', description: 'Optional RFC3339 range start', required: false },
      { name: 'end_time', type: 'string', description: 'Optional RFC3339 range end', required: false },
    ],
    secret_requirements: [
      { id: 'clickhouse_source_credential', description: 'Optional source ClickHouse credential referenced at runtime only', required: false },
      { id: 'clickhouse_target_credential', description: 'Optional target ClickHouse credential referenced at runtime only', required: false },
    ],
  }
  operation.availability.secret_delivery = false
  return operation
}

function sysctlOperation(): OperationDefinition {
  const operation = structuredClone(operationDefinitionFixture)
  operation.metadata.id = 'linux.network.icmp_redirects.runtime_repair'
  operation.metadata.name = 'Server runtime repair'
  operation.metadata.description = 'Repair runtime sysctl from verified state.'
  operation.metadata.parameters = [
    { name: 'check_id', type: 'string', description: 'Check ID', required: true },
    { name: 'target_value', type: 'string', description: 'Target value', required: true },
  ]
  delete operation.metadata.secret_requirements
  return operation
}

describe('PWV1 Operation presentation', () => {
  it('localizes ClickHouse, preserves advanced values, and submits canonical payload structure', async () => {
    const operation = clickhouseOperation()
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operation] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    const create = vi.spyOn(api, 'createOperationRun').mockResolvedValue(operationRunFixture())

    render(<OperationsPage navigate={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: 'ClickHouse 在线迁移' })).toBeTruthy()
    expect(screen.getByText(/Setpoint 会先发现源端与目标端/)).toBeTruthy()
    const source = screen.getByText(/源 ClickHouse/).closest('fieldset')!
    const target = screen.getByText(/目标 ClickHouse/).closest('fieldset')!
    expect(within(source).getByLabelText('主机名或地址 *')).toBeTruthy()
    expect(within(target).getByLabelText('主机名或地址 *')).toBeTruthy()
    expect(screen.getByLabelText('待迁移数据库 *')).toBeTruthy()
    expect(screen.getByLabelText('待迁移表 *')).toBeTruthy()

    const summary = screen.getByText('高级范围选项（可选）')
    const details = summary.closest('details') as HTMLDetailsElement
    expect(details.open).toBe(false)
    fireEvent.click(summary)
    const timeColumn = screen.getByLabelText('事件时间列（可选）') as HTMLInputElement
    fireEvent.change(timeColumn, { target: { value: 'event_time' } })
    fireEvent.change(screen.getByLabelText('时间范围开始（可选，RFC3339）'), { target: { value: '2026-08-01T00:00:00Z' } })
    fireEvent.change(screen.getByLabelText('时间范围结束（可选，RFC3339）'), { target: { value: '2026-08-02T00:00:00Z' } })
    fireEvent.click(summary)
    fireEvent.click(summary)
    expect((screen.getByLabelText('事件时间列（可选）') as HTMLInputElement).value).toBe('event_time')

    fireEvent.change(within(source).getByLabelText('主机名或地址 *'), { target: { value: 'source.internal' } })
    fireEvent.change(within(source).getByLabelText('Native 协议端口（常用 9000 / 9440）'), { target: { value: '9000' } })
    fireEvent.change(within(source).getByLabelText('数据库用户名'), { target: { value: 'reader' } })
    fireEvent.click(within(source).getByLabelText('使用安全 Native 协议'))
    fireEvent.change(within(target).getByLabelText('主机名或地址 *'), { target: { value: 'target.internal' } })
    fireEvent.change(within(target).getByLabelText('Native 协议端口（常用 9000 / 9440）'), { target: { value: '9440' } })
    fireEvent.change(within(target).getByLabelText('数据库用户名'), { target: { value: 'writer' } })
    fireEvent.click(within(target).getByLabelText('使用安全 Native 协议'))
    fireEvent.change(screen.getByLabelText('待迁移数据库 *'), { target: { value: 'events' } })
    fireEvent.change(screen.getByLabelText('待迁移表 *'), { target: { value: 'history, audit' } })
    fireEvent.change(screen.getByLabelText('执行节点'), { target: { value: nodeFixture.id } })
    fireEvent.click(screen.getByRole('button', { name: '生成操作计划' }))

    await waitFor(() => expect(create).toHaveBeenCalledTimes(1))
    const expected = {
      source: { host: 'source.internal', port: 9000, user: 'reader', secure: true },
      target: { host: 'target.internal', port: 9440, user: 'writer', secure: true },
      database: 'events',
      tables: ['history', 'audit'],
      time_column: 'event_time',
      start_time: '2026-08-01T00:00:00Z',
      end_time: '2026-08-02T00:00:00Z',
    }
    expect(create.mock.calls[0][3]).toEqual(expected)
    expect(JSON.stringify(create.mock.calls[0][3])).toBe(JSON.stringify(expected))
  })

  it('shows the sysctl human label while retaining the stable technical identity', async () => {
    const operation = sysctlOperation()
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operation] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })

    render(<OperationsPage navigate={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: 'ICMP Redirect 运行时修复' })).toBeTruthy()
    expect(screen.getByText('linux.network.icmp_redirects.runtime_repair')).toBeTruthy()
  })
})
