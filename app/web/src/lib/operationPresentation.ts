import type { OperationDefinition, OperationState } from '../api/types'

type Presentation = {
  name: string
  description: string
  category?: string
  impact?: string
  labels?: Record<string, string>
  advanced?: string[]
}

const clickhouseID = 'operation.clickhouse.online_migration'
const sysctlID = 'linux.network.icmp_redirects.runtime_repair'

const presentations: Record<string, Presentation> = {
  [clickhouseID]: {
    name: 'ClickHouse 在线迁移',
    description: 'Setpoint 会先发现源端与目标端、执行安全前置检查并生成迁移计划；只有满足安全条件并经过允许的后续动作才会继续。',
    category: '数据迁移',
    impact: '所有写入都必须经过已验证的受控操作计划；实际执行还要求目标满足安全提交条件，并拥有已验证的运行级恢复点。',
    labels: {
      source: '源 ClickHouse',
      target: '目标 ClickHouse',
      'source.host': '主机名或地址',
      'target.host': '主机名或地址',
      'source.port': 'Native 协议端口（常用 9000 / 9440）',
      'target.port': 'Native 协议端口（常用 9000 / 9440）',
      'source.user': '数据库用户名',
      'target.user': '数据库用户名',
      'source.secure': '使用安全 Native 协议',
      'target.secure': '使用安全 Native 协议',
      database: '待迁移数据库',
      tables: '待迁移表',
      time_column: '事件时间列（可选）',
      start_time: '时间范围开始（可选，RFC3339）',
      end_time: '时间范围结束（可选，RFC3339）',
    },
    advanced: ['time_column', 'start_time', 'end_time'],
  },
  [sysctlID]: {
    name: 'ICMP Redirect 运行时修复',
    description: '根据已验证的检查结果修复 ICMP Redirect 运行时 sysctl；不会改写持久化配置。',
    category: 'Linux 运行时修复',
    impact: '仅修复当前运行时值，持久化配置保持不变。',
  },
}

export function operationPresentation(definition: OperationDefinition) {
  const presentation = presentations[definition.metadata.id]
  return {
    name: presentation?.name ?? definition.metadata.name,
    description: presentation?.description ?? definition.metadata.description,
    category: presentation?.category ?? definition.metadata.category,
    impact: presentation?.impact ?? definition.metadata.impact,
  }
}

export function operationNameForID(id: string, fallback: string) {
  return presentations[id]?.name ?? fallback
}

export function operationParameterLabel(operationID: string, path: string, fallback: string) {
  return presentations[operationID]?.labels?.[path] ?? fallback
}

export function operationParameterAdvanced(operationID: string, parameterName: string) {
  return presentations[operationID]?.advanced?.includes(parameterName) ?? false
}

export function operationActivityLabel(state: OperationState) {
  const labels: Record<OperationState, string> = {
    draft: '等待生成操作计划',
    discovering: '正在发现目标',
    prechecking: '正在执行安全前置检查',
    planned: '操作计划已生成',
    awaiting_confirmation: '等待用户确认',
    queued: '已进入执行队列',
    acquiring_lock: '正在获取执行锁',
    creating_restore_point: '正在创建恢复点',
    running: '正在执行受控变更',
    verifying: '正在验证变更结果',
    succeeded: '执行成功',
    blocked: '已被安全条件阻断',
    failed: '执行失败，等待安全处理',
    rolling_back: '正在回滚',
    rolled_back: '已完成回滚',
    rollback_failed: '回滚失败，需要人工处理',
    interrupted: '执行已中断，等待安全处理',
    canceled_before_apply: '已在实际变更前取消',
  }
  return labels[state]
}
