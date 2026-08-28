// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from '../api/client'
import type { OperationDefinition } from '../api/types'
import { nodeFixture, operationDefinitionFixture, operationRunFixture } from '../test/fixtures'
import { buildOperationParameters, OperationsPage } from './OperationsPage'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

function sysctlOperation(): OperationDefinition {
  const operation = structuredClone(operationDefinitionFixture)
  operation.metadata.id = 'linux.network.icmp_redirects.runtime_repair'
  operation.metadata.name = 'ICMP Redirect 运行时修复'
  operation.metadata.category = 'linux_runtime_repair'
  operation.metadata.risk = 'low'
  operation.metadata.parameters = [
    { name: 'check_id', type: 'string', description: '检查项', required: true },
    { name: 'target_value', type: 'string', description: '目标值', required: true },
  ]
  delete operation.metadata.secret_requirements
  operation.availability.secret_delivery = false
  return operation
}

describe('OperationsPage', () => {
  it('shows an empty catalog without rendering a create form', async () => {
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    render(<OperationsPage navigate={vi.fn()} />)
    expect(await screen.findByText('没有已注册的受控操作')).toBeTruthy()
    expect(screen.queryByRole('button', { name: '生成操作计划' })).toBeNull()
  })

  it('renders a sysctl operation when secret_requirements is omitted', async () => {
    const operation = sysctlOperation()
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operation] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })

    render(<OperationsPage navigate={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: 'ICMP Redirect 运行时修复' })).toBeTruthy()
    expect(screen.getByText('linux.network.icmp_redirects.runtime_repair')).toBeTruthy()
    expect(screen.queryByText('运行时秘密边界')).toBeNull()
  })

  it('renders both sysctl and ClickHouse catalog entries', async () => {
    const sysctl = sysctlOperation()
    const clickhouse = structuredClone(operationDefinitionFixture)
    clickhouse.metadata.id = 'operation.clickhouse.online_migration'
    clickhouse.metadata.name = 'ClickHouse 在线迁移'
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [sysctl, clickhouse] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })

    render(<OperationsPage navigate={vi.fn()} />)

    expect(await screen.findByRole('button', { name: /ICMP Redirect 运行时修复/ })).toBeTruthy()
    expect(screen.getByRole('button', { name: /ClickHouse 在线迁移/ })).toBeTruthy()
  })

  it('renders an empty parameter area and builds empty parameters when parameters is omitted', async () => {
    const operation = sysctlOperation()
    delete operation.metadata.parameters
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operation] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })

    render(<OperationsPage navigate={vi.fn()} />)

    await screen.findByRole('heading', { name: 'ICMP Redirect 运行时修复' })
    expect(screen.getByLabelText('操作参数').childElementCount).toBe(0)
    expect(buildOperationParameters(operation, {})).toEqual({ parameters: {} })
  })

  it('renders catalog fields without capability-specific UI branches and submits canonical values', async () => {
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operationDefinitionFixture] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    const create = vi.spyOn(api, 'createOperationRun').mockResolvedValue(operationRunFixture())
    const navigate = vi.fn()
    render(<OperationsPage navigate={navigate} />)

    await screen.findByRole('heading', { name: '示例在线迁移' })
    expect(screen.getByText('计划可用 · 实际执行未开放')).toBeTruthy()
    expect(screen.getByText('product_apply_disabled')).toBeTruthy()
    fireEvent.change(screen.getByLabelText('执行节点'), { target: { value: 'node-1' } })
    fireEvent.change(screen.getByLabelText('源地址 *'), { target: { value: 'source.internal' } })
    fireEvent.change(screen.getByLabelText('源端口'), { target: { value: '9000' } })
    fireEvent.click(screen.getByLabelText('安全连接'))
    fireEvent.change(screen.getByLabelText('数据库 *'), { target: { value: 'events' } })
    fireEvent.change(screen.getByLabelText('数据表 *'), { target: { value: 'history, audit' } })
    fireEvent.click(screen.getByRole('button', { name: '生成操作计划' }))

    await waitFor(() => expect(create).toHaveBeenCalled())
    expect(create.mock.calls[0][3]).toEqual({ source: { host: 'source.internal', port: 9000, secure: true }, database: 'events', tables: ['history', 'audit'] })
    expect(create.mock.calls[0][4]).toEqual([])
    expect(navigate).toHaveBeenCalledWith('/operations/runs/operation-run-1')
  })

  it('fails closed on missing required parameters', async () => {
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operationDefinitionFixture] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    vi.spyOn(api, 'createOperationRun')
    render(<OperationsPage navigate={vi.fn()} />)
    await screen.findByRole('heading', { name: '示例在线迁移' })
    fireEvent.change(screen.getByLabelText('执行节点'), { target: { value: 'node-1' } })
    fireEvent.click(screen.getByRole('button', { name: '生成操作计划' }))
    expect((await screen.findByRole('alert')).textContent).toContain('源地址为必填项')
    expect(api.createOperationRun).not.toHaveBeenCalled()
  })

  it('renders a structured Server 400 without losing the submitted form', async () => {
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [operationDefinitionFixture] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    vi.spyOn(api, 'createOperationRun').mockRejectedValue(new APIError('目标参数无效', 400, 'invalid_request'))
    render(<OperationsPage navigate={vi.fn()} />)
    await screen.findByRole('heading', { name: '示例在线迁移' })
    fireEvent.change(screen.getByLabelText('执行节点'), { target: { value: 'node-1' } })
    fireEvent.change(screen.getByLabelText('源地址 *'), { target: { value: 'source.internal' } })
    fireEvent.change(screen.getByLabelText('数据库 *'), { target: { value: 'events' } })
    fireEvent.change(screen.getByLabelText('数据表 *'), { target: { value: 'history' } })
    fireEvent.click(screen.getByRole('button', { name: '生成操作计划' }))
    expect((await screen.findByRole('alert')).textContent).toBe('目标参数无效')
    expect((screen.getByLabelText('数据库 *') as HTMLInputElement).value).toBe('events')
  })

  it('does not render plaintext secret controls or write browser storage', async () => {
    const requiredSecret = structuredClone(operationDefinitionFixture)
    requiredSecret.metadata.secret_requirements![0].required = true
    vi.spyOn(api, 'operations').mockResolvedValue({ operations: [requiredSecret] })
    vi.spyOn(api, 'nodes').mockResolvedValue({ nodes: [nodeFixture] })
    const local = vi.spyOn(Storage.prototype, 'setItem')
    render(<OperationsPage navigate={vi.fn()} />)
    await screen.findByText('运行时秘密交付尚未开放；页面不接收密码、Token 或私钥。')
    expect(screen.queryByLabelText(/密码|Token|私钥/)).toBeNull()
    expect((screen.getByRole('button', { name: '生成操作计划' }) as HTMLButtonElement).disabled).toBe(true)
    expect(local).not.toHaveBeenCalled()
  })
})

describe('buildOperationParameters', () => {
  it('returns an empty object when metadata.parameters is omitted', () => {
    const operation = structuredClone(operationDefinitionFixture)
    delete operation.metadata.parameters
    expect(buildOperationParameters(operation, {})).toEqual({ parameters: {} })
  })

  it('rejects unsupported metadata instead of accepting arbitrary JSON', () => {
    const invalid = structuredClone(operationDefinitionFixture)
    invalid.metadata.parameters = [{ name: 'payload', type: 'object', description: 'payload', required: true, fields: [] }]
    expect(buildOperationParameters(invalid, {})).toMatchObject({ error: expect.stringContaining('不支持') })
  })

  it('rejects secret-like catalog fields before rendering a plaintext control', () => {
    const invalid = structuredClone(operationDefinitionFixture)
    invalid.metadata.parameters![0].fields!.push({ name: 'access_token', type: 'string', description: 'token', required: false })
    expect(buildOperationParameters(invalid, {})).toMatchObject({ error: expect.stringContaining('不支持') })
  })

  it('rejects fractional values for integer fields', () => {
    expect(buildOperationParameters(operationDefinitionFixture, {
      'source.host': 'source.internal', 'source.port': '9000.5', database: 'events', tables: 'history',
    })).toMatchObject({ error: expect.stringContaining('必须是整数') })
  })
})
