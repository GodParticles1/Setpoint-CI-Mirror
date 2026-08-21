import type { NodeBootstrapApplyResponse, NodeBootstrapGatewayInput, NodeBootstrapProbeResponse } from './bootstrap-types'
import type {
  CancelCheckRunResponse,
  CheckBundle,
  CheckDefinition,
  CheckPolicy,
  CheckRun,
  CheckSelection,
  DashboardSummary,
  GranularCheckDefinition,
  Node,
  OperationDefinition,
  OperationRun,
  OperationTarget,
  RuntimeSettings,
  SecretRef,
  Site,
} from './types'

export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string,
  ) {
    super(message)
    this.name = 'APIError'
  }
}

async function request<T>(path: string, init: RequestInit = {}, timeoutMs = 15_000): Promise<T> {
  const controller = new AbortController()
  const timeout = window.setTimeout(() => controller.abort(), timeoutMs)
  const external = init.signal
  const abort = () => controller.abort()
  external?.addEventListener('abort', abort, { once: true })
  try {
    const response = await fetch(path, {
      ...init,
      signal: controller.signal,
      headers: init.body
        ? { 'Content-Type': 'application/json', ...init.headers }
        : init.headers,
    })
    if (!response.ok) {
      let code = 'request_failed'
      let message = `请求失败（${response.status}）`
      try {
        const body = (await response.json()) as { error?: { code?: string; message?: string } }
        code = body.error?.code || code
        message = body.error?.message || message
      } catch {
        // The status remains authoritative when an upstream returns a non-JSON error.
      }
      throw new APIError(message, response.status, code)
    }
    if (response.status === 204) return undefined as T
    return (await response.json()) as T
  } catch (error) {
    if (error instanceof APIError) throw error
    if (controller.signal.aborted) throw new APIError('请求已取消或超时', 0, 'request_aborted')
    throw new APIError(error instanceof Error ? error.message : '网络请求失败', 0, 'network_error')
  } finally {
    window.clearTimeout(timeout)
    external?.removeEventListener('abort', abort)
  }
}

export const api = {
  dashboard: (signal?: AbortSignal) => request<DashboardSummary>('/api/v1/dashboard/summary', { signal }),
  sites: (signal?: AbortSignal) => request<{ sites: Site[] }>('/api/v1/sites', { signal }),
  nodes: (signal?: AbortSignal) => request<{ nodes: Node[] }>('/api/v1/nodes', { signal }),
  checks: (signal?: AbortSignal) => request<{ checks: CheckDefinition[] }>('/api/v1/checks', { signal }),
  checkDefinitions: (signal?: AbortSignal) => request<{ definitions: GranularCheckDefinition[] }>('/api/v1/check-definitions', { signal }),
  checkBundles: (signal?: AbortSignal) => request<{ bundles: CheckBundle[] }>('/api/v1/check-bundles', { signal }),
  checkPolicies: (signal?: AbortSignal) => request<{ policies: CheckPolicy[] }>('/api/v1/check-policies', { signal }),
  runs: (offset = 0, signal?: AbortSignal) =>
    request<{ runs: CheckRun[]; limit: number; offset: number }>(`/api/v1/check-runs?limit=50&offset=${offset}`, { signal }),
  run: (id: string, signal?: AbortSignal) => request<CheckRun>(`/api/v1/check-runs/${encodeURIComponent(id)}`, { signal }),
  settings: (signal?: AbortSignal) => request<RuntimeSettings>('/api/v1/settings', { signal }),
  probeNodeBootstrap: (address: string, port: number, username: string, password: string, gateway?: NodeBootstrapGatewayInput) =>
    request<NodeBootstrapProbeResponse>('/api/v1/node-bootstrap/probe', {
      method: 'POST',
      body: JSON.stringify({ address, port, username, password, gateway }),
    }, 20_000),
  applyNodeBootstrap: (
    address: string,
    port: number,
    username: string,
    password: string,
    expectedHostKeyFingerprint: string,
    siteId: string,
    gateway?: NodeBootstrapGatewayInput,
    expectedGatewayHostKeyFingerprint?: string,
  ) => request<NodeBootstrapApplyResponse>('/api/v1/node-bootstrap/apply', {
    method: 'POST',
    body: JSON.stringify({
      address,
      port,
      username,
      password,
      gateway,
      expected_host_key_fingerprint: expectedHostKeyFingerprint,
      expected_gateway_host_key_fingerprint: expectedGatewayHostKeyFingerprint || undefined,
      site_id: siteId || undefined,
    }),
  }, 70_000),
  createSite: (name: string, description: string, trustedRoots: string[], idempotencyKey: string) =>
    request<Site>('/api/v1/sites', {
      method: 'POST',
      body: JSON.stringify({
        api_version: 'setpoint.io/v1',
        kind: 'Site',
        metadata: { idempotency_key: idempotencyKey },
        spec: { name, description, trusted_executable_roots: trustedRoots },
      }),
    }),
  updateSite: (id: string, name: string, description: string, trustedRoots: string[]) =>
    request<Site>(`/api/v1/sites/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify({ spec: { name, description, trusted_executable_roots: trustedRoots } }),
    }),
  deleteSite: (id: string) => request<void>(`/api/v1/sites/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  updateNode: (id: string, siteId: string, tags: string[], notes: string, trustedRoots: string[]) =>
    request<Node>(`/api/v1/nodes/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify({ site_id: siteId, tags, notes, trusted_executable_roots: trustedRoots }),
    }),
  createRun: (name: string, nodeIds: string[], selection: CheckSelection, idempotencyKey: string) =>
    request<CheckRun>('/api/v1/check-runs', {
      method: 'POST',
      body: JSON.stringify({
        api_version: 'setpoint.io/v1',
        kind: 'ReadOnlyCheckRun',
        metadata: { idempotency_key: idempotencyKey, name },
        spec: {
          node_ids: nodeIds,
          check_ids: selection.checkIds,
          bundle_ids: selection.bundleIds,
          policy_ids: selection.policyIds,
          parameters: {},
        },
      }),
    }),
  cancelRun: (id: string) => request<CancelCheckRunResponse>(`/api/v1/check-runs/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
  operations: (signal?: AbortSignal) => request<{ operations: OperationDefinition[] }>('/api/v1/operations', { signal }),
  operation: (id: string, signal?: AbortSignal) => request<OperationDefinition>(`/api/v1/operations/${encodeURIComponent(id)}`, { signal }),
  operationRuns: (offset = 0, signal?: AbortSignal) =>
    request<{ runs: OperationRun[]; limit: number; offset: number }>(`/api/v1/operation-runs?limit=50&offset=${offset}`, { signal }),
  operationRun: (id: string, signal?: AbortSignal) => request<OperationRun>(`/api/v1/operation-runs/${encodeURIComponent(id)}`, { signal }),
  createOperationRun: (
    operationId: string,
    nodeId: string,
    targets: OperationTarget[],
    parameters: Record<string, unknown>,
    secretRefs: SecretRef[],
    idempotencyKey: string,
  ) => request<OperationRun>('/api/v1/operation-runs', {
    method: 'POST',
    body: JSON.stringify({
      api_version: 'setpoint.io/v1',
      kind: 'OperationRun',
      metadata: { idempotency_key: idempotencyKey },
      spec: { operation_id: operationId, node_id: nodeId, targets, parameters, secret_refs: secretRefs },
    }),
  }),
  confirmOperationRun: (id: string, planDigest: string, idempotencyKey: string) =>
    request<OperationRun>(`/api/v1/operation-runs/${encodeURIComponent(id)}/confirm`, {
      method: 'POST',
      body: JSON.stringify({ idempotency_key: idempotencyKey, plan_digest: planDigest }),
    }),
  cancelOperationRun: (id: string) => request<OperationRun>(`/api/v1/operation-runs/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
}
