import type { AgentCallbackStatus } from './bootstrap-types'

export async function fetchAgentCallbackStatus(signal?: AbortSignal): Promise<AgentCallbackStatus> {
  const response = await fetch('/api/v1/node-bootstrap/callback-status', { signal })
  if (!response.ok) {
    throw new Error(`读取 Agent 回调状态失败（${response.status}）`)
  }
  return (await response.json()) as AgentCallbackStatus
}
