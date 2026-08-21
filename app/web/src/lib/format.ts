import type { ItemStatus, OperationState, RunPhase } from '../api/types'

export const itemLabels: Record<ItemStatus, string> = {
  safe: '安全',
  unsafe: '不安全',
  manual_review: '人工复核',
  error: '检查错误',
  not_applicable: '不适用',
}

export const phaseLabels: Record<RunPhase, string> = {
  pending: '等待执行',
  running: '执行中',
  completed: '已完成',
  partial_failed: '部分失败',
  canceled: '已取消',
}

export const operationStateLabels: Record<OperationState, string> = {
  draft: '等待规划', discovering: '发现目标', prechecking: '前置检查', planned: '计划已生成',
  awaiting_confirmation: '等待确认', queued: '等待执行', acquiring_lock: '获取锁',
  creating_restore_point: '创建恢复点', running: '执行中', verifying: '验证中', succeeded: '已完成',
  blocked: '已阻断', failed: '失败', rolling_back: '回滚中', rolled_back: '已回滚',
  rollback_failed: '回滚失败', interrupted: '已中断', canceled_before_apply: '执行前已取消',
}

export function operationTerminal(state: OperationState): boolean {
  return ['succeeded', 'blocked', 'canceled_before_apply', 'rolled_back', 'rollback_failed'].includes(state)
}

export function dateTime(value?: string): string {
  if (!value) return '暂无'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return '未知时间'
  if (parsed.getUTCFullYear() <= 1) return '暂无'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(parsed)
}

let fallbackSequence = 0

export function newIdempotencyKey(): string {
  const cryptoAPI = globalThis.crypto
  if (typeof cryptoAPI?.randomUUID === 'function') {
    return cryptoAPI.randomUUID()
  }
  if (typeof cryptoAPI?.getRandomValues === 'function') {
    const bytes = cryptoAPI.getRandomValues(new Uint8Array(16))
    return `ui-${Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('')}`
  }
  fallbackSequence = (fallbackSequence + 1) % Number.MAX_SAFE_INTEGER
  const random = Math.random().toString(36).slice(2, 10)
  return `ui-${Date.now().toString(36)}-${fallbackSequence.toString(36)}-${random}`
}

export function shortID(value: string): string {
  return value.length > 13 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value
}

export function isTerminal(phase: RunPhase): boolean {
  return phase === 'completed' || phase === 'partial_failed' || phase === 'canceled'
}

export function cleanTags(value: string): string[] {
  return [...new Set(value.split(',').map((tag) => tag.trim()).filter(Boolean))]
}
