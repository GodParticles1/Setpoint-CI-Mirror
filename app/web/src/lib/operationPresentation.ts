import type { OperationDefinition, OperationParameter, OperationParameterField, OperationState } from '../api/types'

type MetadataPresentation = OperationDefinition['metadata'] & { display_category?: string }
type PresentableField = (OperationParameter | OperationParameterField) & { label?: string }
type AvailabilityPresentation = OperationDefinition['availability'] & { block_reason?: string }

const advancedRangeParameters = new Set(['time_column', 'start_time', 'end_time'])

export function operationPresentation(definition: OperationDefinition) {
  const metadata = definition.metadata as MetadataPresentation
  return {
    name: metadata.name,
    description: metadata.description,
    category: metadata.display_category?.trim() || metadata.category,
    impact: metadata.impact,
  }
}

export function operationParameterLabel(field: OperationParameter | OperationParameterField) {
  const presentable = field as PresentableField
  return presentable.label?.trim() || presentable.description?.trim() || presentable.name
}

export function operationParameterAdvanced(parameter: OperationParameter) {
  return !parameter.required && advancedRangeParameters.has(parameter.name)
}

export function operationBlockReason(definition: OperationDefinition) {
  const availability = definition.availability as AvailabilityPresentation
  return availability.block_reason?.trim() || ''
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
