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
    category: '数据迁移',
    name: 'ClickHouse 在线迁移',
    version: '0.2.0',
    description: '发现源端与目标端，执行安全前置检查并生成经过验证的在线迁移计划。',
    impact: '所有写入都必须经过已验证的受控操作计划，并要求目标满足安全提交条件。',
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
      { id: 'clickhouse_source_credential', description: '源 ClickHouse 凭据引用', required: false },
      { id: 'clickhouse_target_credential', description: '目标 ClickHouse 凭据引用', required: false },
    ],
  }
  operation.availability.secret_delivery = false
  return operation
}

function sysctlOperation(): OperationDefinition {
  const operation = structuredClone(operationDefinitionFixture)
  operation.metadata.id = 'linux.network.icmp_redirects.runtime_repair'
  operation.metadata.name = 'ICMP Redirect 运行时修复'
  operation.metadata.category = 'Linux 运行时修复'
  operation.metadata.description = '根据已验证检查结果修复 ICMP Redirect 运行时值，不改写持久化配置。'
  operation.metadata.impact = '仅修改当前运行时 sysctl，持久化配置保持不变。'
  operation.metadata.parameters = [
    { name: 'check_id', type: 'string', description: '待修复检查项', required: true },
    { name: 'target_value', type: 'string', description: '已冻结的检查建议值', required: true },
  ]
  delete operation.metadata.secret_requirements
  return operation
}

function syntheticThirdOperation(): OperationDefinition {
  const operation = structuredClone(operationDefinitionFixture)
  operation.metadata = {
    ...operation.metadata,
    id: 'operation.synthetic.future_plugin',
    category: '存储维护',
    name: '未来插件安全维护',
    description: '这是由未来插件 metadata 提供的中文操作说明。',
    impact: '只生成受控维护计划，并保留现有数据安全边界。',
    parameters: [
      { name: 'target', type: 'object', description: '维护目标', required: true, fields: [
        { name: 'host', type: 'string', description: '目标主机', required: true },
      ] },
      { name: 'scope', type: 'string', description: '维护范围', required: true },
    ],
    secret_requirements: [],
  }
  return operation
}

describe('PWV1 Operation presentation', () => {
  it('uses ClickHouse metadata labels, preserves advanced values, and submits canonical payload structure', async () => {
    const operation = clickhouseOperation()
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operation] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    const create = vi.spyOn(api, 'createOperationRun').mockResolvedValue(operationRunFixture())

    render(<OperationsPage navigate={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: 'ClickHouse 在线迁移' })).toBeTruthy()
    expect(screen.getByText('发现源端与目标端，执行安全前置检查并生成经过验证的在线迁移计划。')).toBeTruthy()
    const source = screen.getByRole('group', { name: /源 ClickHouse/ })
    const target = screen.getByRole('group', { name: /目标 ClickHouse/ })
    expect(within(source).getByLabelText('主机名或地址 *')).toBeTruthy()
    expect(within(target).getByLabelText('主机名或地址 *')).toBeTruthy()
    expect(screen.getByLabelText('待迁移数据库 *')).toBeTruthy()
    expect(screen.getByLabelText('待迁移表 *')).toBeTruthy()
    expect(screen.getByText('源 ClickHouse 凭据引用（可选）')).toBeTruthy()
    expect(screen.getByText('目标 ClickHouse 凭据引用（可选）')).toBeTruthy()

    const summary = screen.getByText('高级范围选项（可选）')
    const details = summary.closest('details') as HTMLDetailsElement
    expect(details.open).toBe(false)
    fireEvent.click(summary)
    fireEvent.change(screen.getByLabelText('事件时间列（可选）'), { target: { value: 'event_time' } })
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
  })

  it('renders backend Chinese sysctl metadata without hardcoded English replacements', async () => {
    const operation = sysctlOperation()
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operation] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })

    render(<OperationsPage navigate={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: 'ICMP Redirect 运行时修复' })).toBeTruthy()
    expect(screen.getByLabelText('待修复检查项 *')).toBeTruthy()
    expect(screen.getByLabelText('已冻结的检查建议值 *')).toBeTruthy()
    expect(screen.queryByText('Persisted ICMP Redirect Check result being repaired')).toBeNull()
    expect(screen.queryByText('Frozen Check recommendation')).toBeNull()
    expect(screen.getByText('linux.network.icmp_redirects.runtime_repair')).toBeTruthy()
  })

  it('renders a never-before-seen Operation entirely from its Chinese metadata', async () => {
    const operation = syntheticThirdOperation()
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operation] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })

    render(<OperationsPage navigate={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: '未来插件安全维护' })).toBeTruthy()
    expect(screen.getByText('这是由未来插件 metadata 提供的中文操作说明。')).toBeTruthy()
    expect(screen.getAllByText('只生成受控维护计划，并保留现有数据安全边界。').length).toBeGreaterThan(0)
    expect(screen.getAllByText('存储维护 · v1.0.0')).toHaveLength(2)
    expect(screen.getByText(/维护目标/)).toBeTruthy()
    expect(screen.getByLabelText('目标主机 *')).toBeTruthy()
    expect(screen.getByLabelText('维护范围 *')).toBeTruthy()
    expect(screen.getByText('operation.synthetic.future_plugin')).toBeTruthy()
  })
})
