// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from './client'

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('API client', () => {
  it('uses the caller-provided idempotency key for create requests', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })))
    vi.stubGlobal('fetch', fetchMock)

    await api.createRun('批次', ['node-1'], { checkIds: ['test.item'], bundleIds: [], policyIds: [] }, 'run-key')
    await api.createSite('站点', '说明', ['/opt/company/nginx/bin'], 'site-key')

    const runBody = JSON.parse(String(fetchMock.mock.calls[0][1]?.body))
    const siteBody = JSON.parse(String(fetchMock.mock.calls[1][1]?.body))
    expect(runBody.metadata.idempotency_key).toBe('run-key')
    expect(runBody.spec).toMatchObject({ check_ids: ['test.item'], bundle_ids: [], policy_ids: [] })
    expect(siteBody.metadata.idempotency_key).toBe('site-key')
    expect(siteBody.spec.trusted_executable_roots).toEqual(['/opt/company/nginx/bin'])
  })

  it('preserves structured 500 errors', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(
      JSON.stringify({ error: { code: 'server_failure', message: '服务暂不可用' } }),
      { status: 500, headers: { 'Content-Type': 'application/json' } },
    )))
    await expect(api.dashboard()).rejects.toMatchObject({ status: 500, code: 'server_failure', message: '服务暂不可用' })
  })

  it('uses frozen operation targets and exact plan digest in planning requests', async () => {
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })))
    vi.stubGlobal('fetch', fetchMock)
    await api.createOperationRun('operation.test', 'node-1', [{ kind: 'node', node_id: 'node-1' }], { database: 'events' }, [], 'operation-key')
    await api.confirmOperationRun('run-1', `sha256:${'a'.repeat(64)}`, 'confirm-key')
    await api.cancelOperationRun('run-1')
    const createBody = JSON.parse(String(fetchMock.mock.calls[0][1]?.body))
    const confirmBody = JSON.parse(String(fetchMock.mock.calls[1][1]?.body))
    expect(createBody).toMatchObject({ kind: 'OperationRun', metadata: { idempotency_key: 'operation-key' }, spec: { node_id: 'node-1', targets: [{ kind: 'node', node_id: 'node-1' }], secret_refs: [] } })
    expect(confirmBody).toEqual({ idempotency_key: 'confirm-key', plan_digest: `sha256:${'a'.repeat(64)}` })
    expect(fetchMock.mock.calls[2][0]).toBe('/api/v1/operation-runs/run-1/cancel')
  })

  it('classifies an unresolved request as an abort after the timeout', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn((_path: string, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
    })))

    const pending = api.dashboard()
    const settled = pending.then(() => null, (error: unknown) => error)
    await vi.advanceTimersByTimeAsync(15_000)
    const error = await settled
    expect(error).toMatchObject({ status: 0, code: 'request_aborted' })
  })
})
