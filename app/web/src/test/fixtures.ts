import type { CheckItem, CheckRun, GranularCheckDefinition, Node, OperationDefinition, OperationRun, Site } from '../api/types'

export const nodeFixture: Node = {
  id: 'node-1', hostname: 'node-one', os: 'linux', os_version: 'test', arch: 'amd64', agent_version: 'test',
  observed_source_address: '192.0.2.10', tags: [], notes: '', registered_at: '2026-08-05T00:00:00Z',
	trusted_executable_roots: [], last_seen_at: '2026-08-05T00:00:00Z', status: 'online',
}

export const checkFixture: GranularCheckDefinition = {
	id: 'test.item', plugin_id: 'linux.test', plugin_version: '1.0.0', category: 'Linux',
	name: 'Linux 测试检查', description: 'test', recommended_value: 'expected', risk: 'low',
	supported_systems: ['linux'], parameters: [], source_refs: ['fixture:test.item'],
}

export const siteFixture: Site = {
  id: 'site-1', name: '测试站点', description: 'test', node_count: 0,
	trusted_executable_roots: [],
  created_at: '2026-08-05T00:00:00Z', updated_at: '2026-08-05T00:00:00Z',
}

export function runFixture(phase: CheckRun['status']['phase'] = 'running', items: CheckItem[] = []): CheckRun {
  const terminal = phase === 'completed' || phase === 'partial_failed' || phase === 'canceled'
  return {
    api_version: 'setpoint.io/v1', kind: 'ReadOnlyCheckRun',
    metadata: { id: 'run-1', idempotency_key: 'server-key', name: '测试批次', created_at: '2026-08-05T00:00:00Z' },
    spec: { node_ids: ['node-1'], check_ids: ['linux.test'], parameters: {} },
    status: {
      phase,
      counts: {
        total_tasks: 1, pending_tasks: terminal ? 0 : 1, running_tasks: 0, completed_tasks: terminal ? 1 : 0,
        canceled_tasks: phase === 'canceled' ? 1 : 0,
        safe: items.filter((item) => item.status === 'safe').length,
        unsafe: items.filter((item) => item.status === 'unsafe').length,
        manual_review: items.filter((item) => item.status === 'manual_review').length,
        error: items.filter((item) => item.status === 'error').length,
        not_applicable: items.filter((item) => item.status === 'not_applicable').length,
      },
      updated_at: phase === 'running' ? '2026-08-05T00:00:00Z' : '2026-08-05T00:01:00Z',
    },
    tasks: terminal ? [{
      api_version: 'setpoint.io/v1', kind: 'ReadOnlyCheckTask',
      metadata: { id: 'task-1', idempotency_key: 'task-key', created_at: '2026-08-05T00:00:00Z' },
      spec: { node_id: 'node-1', plugin_id: 'linux.test', parameters: {} },
      status: { phase: 'succeeded', attempt: 1, updated_at: '2026-08-05T00:01:00Z' },
      result: {
        plugin_id: 'linux.test', plugin_version: '1.0.0', state: 'completed',
        started_at: '2026-08-05T00:00:30Z', completed_at: '2026-08-05T00:01:00Z', items,
      },
    }] : [],
  }
}

export function itemFixture(status: CheckItem['status']): CheckItem {
  return {
    id: `item.${status}`, status, name: `检查项 ${status}`, current_value: 'current', recommended_value: 'expected',
    risk: 'low', risk_description: 'risk', remediation: 'remediation', evidence_summary: 'bounded evidence',
    review_reason: status === 'manual_review' ? '需要核对现场策略' : undefined,
    applicable: status !== 'not_applicable', supports_automatic_fix: false, supports_rollback: false,
    requires_restart: false, may_affect_connection: false, may_affect_business: false,
    executed_at: '2026-08-05T00:01:00Z',
    error: status === 'error' ? { code: 'test_error', message: 'test failure' } : undefined,
  }
}

export const operationDefinitionFixture: OperationDefinition = {
  api_version: 'setpoint.io/v1',
  kind: 'OperationDefinition',
  metadata: {
    id: 'operation.example.transfer', category: 'data_migration', name: '示例在线迁移', version: '1.0.0',
    description: '目录驱动的测试能力', risk: 'high', impact: '只生成计划，不执行实际变更', supported_systems: ['linux'],
    parameters: [
      { name: 'source', type: 'object', description: '源端点', required: true, fields: [
        { name: 'host', type: 'string', description: '源地址', required: true },
        { name: 'port', type: 'integer', description: '源端口', required: false },
        { name: 'secure', type: 'boolean', description: '安全连接', required: false },
      ] },
      { name: 'database', type: 'string', description: '数据库', required: true },
      { name: 'tables', type: 'string[]', description: '数据表', required: true },
    ],
    secret_requirements: [{ id: 'source_credential', description: '源端凭据引用', required: false }],
  },
  capability_digest: `sha256:${'a'.repeat(64)}`,
  availability: { planning: true, apply: false, block_code: 'product_apply_disabled', secret_delivery: false },
}

export function operationRunFixture(state: OperationRun['status']['state'] = 'awaiting_confirmation'): OperationRun {
  return {
    api_version: 'setpoint.io/v1', kind: 'OperationRun',
    metadata: { id: 'operation-run-1', idempotency_key: 'operation-key', created_at: '2026-08-13T00:00:00Z' },
    spec: {
      operation_id: operationDefinitionFixture.metadata.id, operation_version: '1.0.0', capability_digest: operationDefinitionFixture.capability_digest,
      node_id: 'node-1', targets: [{ kind: 'node', node_id: 'node-1' }],
      parameters: { source: { host: 'source.internal', port: 9000 }, database: 'events', tables: ['history'] },
    },
    status: { state, checkpoint: state === 'awaiting_confirmation' ? 'plan_ready' : state, task_id: 'task-1', updated_at: '2026-08-13T00:01:00Z', apply_available: false },
    discovery: { applicable: true, summary: '已冻结两个物理端点', targets: [{ kind: 'node', node_id: 'node-1' }], snapshot: { schema_version: 'v1', payload: {} } },
    precheck: { passed: true, summary: '前置检查通过', snapshot: { schema_version: 'v1', payload: {} } },
    plan: {
      schema_version: 'v1', summary: '分阶段迁移一个表', execution: { schema_version: 'v1', payload: {} },
      steps: [{ id: 'step-1', name: '复制分区', target: { kind: 'data_object', component: 'clickhouse', resource: 'events.history' }, action: 'copy', checkpoint: 'copy_ready', writes: true, retry_safe: false }],
    },
    impact: { summary: '目标端会写入数据', risk: 'high', changes: [], requires_downtime: false, requires_write_fence: true, estimated_duration: 1_000_000_000, estimated_data_change_bytes: 1024 },
    plan_digest: `sha256:${'b'.repeat(64)}`,
  }
}
