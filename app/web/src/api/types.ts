export type ItemStatus = 'safe' | 'unsafe' | 'manual_review' | 'error' | 'not_applicable'
export type RunPhase = 'pending' | 'running' | 'completed' | 'partial_failed' | 'canceled'

export interface Failure {
  code: string
  message: string
}

export interface TrustedExecutableRoot {
  path: string
  scope: 'site' | 'node'
  source: string
  validation_status: 'pending_agent_validation'
}

export interface Site {
  id: string
  name: string
  description: string
  trusted_executable_roots: TrustedExecutableRoot[]
  node_count: number
  created_at: string
  updated_at: string
}

export interface Node {
  id: string
  hostname: string
  os: string
  os_version: string
  arch: string
  agent_version: string
  observed_source_address: string
  site_id?: string
  site_name?: string
  tags: string[]
  notes: string
  trusted_executable_roots: TrustedExecutableRoot[]
  registered_at: string
  last_seen_at: string
  status: 'online' | 'offline'
}

export interface CheckItemDefinition {
  id: string
  name: string
  description: string
  recommended_value: string
}

export interface CheckDefinition {
  id: string
  category: string
  name: string
  version: string
  description: string
  mode: 'read_only'
  risk: 'low' | 'medium' | 'high' | 'critical'
  impact: string
  supported_systems: string[]
  checks: CheckItemDefinition[]
}

export interface GranularCheckDefinition {
  id: string
  plugin_id: string
  plugin_version: string
  category: string
  name: string
  description: string
  recommended_value: string
  risk: 'low' | 'medium' | 'high' | 'critical'
  supported_systems: string[]
  parameters: Array<{ name: string; type: string; description: string; required: boolean; options?: string[] }>
  source_refs?: string[]
}

export interface CheckBundle {
  id: string
  name: string
  description: string
  category: string
  check_ids: string[]
}

export interface CheckPolicy {
  id: string
  name: string
  description: string
  check_ids?: string[]
  bundle_ids?: string[]
}

export interface CheckSelection {
  checkIds: string[]
  bundleIds: string[]
  policyIds: string[]
}

export interface CheckItem {
  id: string
  status: ItemStatus
  name: string
  current_value: string
  recommended_value: string
  risk: string
  risk_description: string
  remediation: string
  evidence_summary: string
  review_reason?: string
  applicable: boolean
  supports_automatic_fix: boolean
  supports_rollback: boolean
  requires_restart: boolean
  may_affect_connection: boolean
  may_affect_business: boolean
  executed_at: string
  error?: Failure
}

export interface RemediationConstraints {
  options?: string[]
  min?: number
  max?: number
  pattern?: string
}

export type RemediationAvailability = 'actionable' | 'manual_only'

export interface RemediationOffer {
  check_run_id: string
  task_id: string
  check_id: string
  node_id: string
  current_value: string
  existing_recommended_value: string
  recommended_value_for_this_run: string
  recommendation_reason: string
  availability: RemediationAvailability
  editable: boolean
  parameter_type?: string
  constraints: RemediationConstraints
  supports_automatic_fix: boolean
  supports_rollback: boolean
  risk: string
  requires_restart: boolean
  may_affect_connection: boolean
  may_affect_business: boolean
  operation_id?: string
  operation_parameters?: Record<string, string>
  block_reason?: string
}

export interface TaskResource {
  api_version: string
  kind: string
  metadata: { id: string; idempotency_key: string; created_at: string }
  spec: { node_id: string; plugin_id: string; parameters: Record<string, unknown> }
  status: {
    phase: string
    attempt: number
    updated_at: string
    last_error?: Failure
  }
  result?: {
    plugin_id: string
    plugin_version: string
    state: 'completed' | 'error'
    started_at: string
    completed_at: string
    items: CheckItem[]
    error?: Failure
  }
}

export interface RunCounts {
  total_tasks: number
  pending_tasks: number
  running_tasks: number
  completed_tasks: number
  canceled_tasks: number
  safe: number
  unsafe: number
  manual_review: number
  error: number
  not_applicable: number
}

export interface CheckRun {
  api_version: string
  kind: string
  metadata: { id: string; idempotency_key: string; name: string; created_at: string }
  spec: { node_ids: string[]; check_ids: string[]; bundle_ids?: string[]; policy_ids?: string[]; parameters?: Record<string, unknown> }
  status: { phase: RunPhase; counts: RunCounts; updated_at: string }
  tasks?: TaskResource[]
  remediation_offers?: RemediationOffer[]
}

export type CancelOutcome = 'canceled' | 'cancel_requested' | 'already_terminal' | 'failed'

export interface CancelTaskResult {
  task_id: string
  outcome: CancelOutcome
  phase: string
  error?: Failure
}

export interface CancelReport {
  total_tasks: number
  canceled_tasks: number
  cancel_requested_tasks: number
  already_terminal_tasks: number
  failed_tasks: number
  results: CancelTaskResult[]
}

export interface CancelCheckRunResponse {
  run: CheckRun
  cancel_report: CancelReport
}

export interface DashboardSummary {
  nodes_total: number
  nodes_online: number
  nodes_offline: number
  recent_runs: number
  safe: number
  unsafe: number
  manual_review: number
  error: number
  not_applicable: number
  last_check_at?: string
}

export interface RuntimeSettings {
  offline_after: string
  minimum_refresh_interval: string
  recommended_refresh_interval: string
  maximum_run_tasks: number
}

export type OperationRisk = 'low' | 'medium' | 'high' | 'critical'
export type OperationState =
  | 'draft' | 'discovering' | 'prechecking' | 'planned' | 'awaiting_confirmation'
  | 'queued' | 'acquiring_lock' | 'creating_restore_point' | 'running' | 'verifying'
  | 'succeeded' | 'blocked' | 'failed' | 'rolling_back' | 'rolled_back'
  | 'rollback_failed' | 'interrupted' | 'canceled_before_apply'

export interface OperationParameterField {
  name: string
  type: 'string' | 'integer' | 'boolean' | 'string[]'
  description: string
  required: boolean
  options?: string[]
}

export interface OperationParameter {
  name: string
  type: 'string' | 'integer' | 'boolean' | 'string[]' | 'object'
  description: string
  required: boolean
  options?: string[]
  fields?: OperationParameterField[]
}

export interface SecretRequirement {
  id: string
  description: string
  required: boolean
}

export interface OperationDefinition {
  api_version: 'setpoint.io/v1'
  kind: 'OperationDefinition'
  metadata: {
    id: string
    category: string
    name: string
    version: string
    description: string
    risk: OperationRisk
    impact: string
    supported_systems: string[]
    parameters?: OperationParameter[]
    secret_requirements?: SecretRequirement[]
  }
  capability_digest: string
  availability: {
    planning: boolean
    apply: boolean
    block_code: string
    secret_delivery: boolean
  }
}

export interface OperationTarget {
  kind: 'site' | 'node' | 'component' | 'data_object'
  site_id?: string
  node_id?: string
  component?: string
  resource?: string
}

export interface SecretRef {
  requirement_id: string
  reference: string
}

export interface OperationFinding {
  code: string
  severity: 'info' | 'warning' | 'blocking'
  summary: string
  detail?: string
  target?: OperationTarget
}

export interface OperationEvidenceRef {
  id: string
  kind: string
  sha256?: string
}

export interface OperationArtifact {
  schema_version: string
  payload: unknown
}

export interface OperationRestorePoint {
  id: string
  provider_id: string
  operation_id: string
  run_id: string
  status: 'created' | 'verified' | 'restored' | 'invalid'
  targets: OperationTarget[]
  created_at: string
  expires_at?: string
  manifest: OperationArtifact
  evidence?: OperationEvidenceRef[]
}

export interface OperationApplyResult {
  changed: boolean
  checkpoint: string
  state: OperationArtifact
  evidence?: OperationEvidenceRef[]
}

export interface OperationVerification {
  passed: boolean
  summary: string
  findings?: OperationFinding[]
  evidence?: OperationEvidenceRef[]
}

export interface OperationRollbackResult {
  restored: boolean
  checkpoint: string
  state: OperationArtifact
  evidence?: OperationEvidenceRef[]
}

export type OperationBatchMemberState = 'pending' | 'confirmed' | 'suppressed_canceled'

export interface OperationBatchConfirmationMember {
  ordinal: number
  identity: { task_id: string; check_id: string; node_id: string }
  run_id: string
  plan_digest: string
  state: OperationBatchMemberState
  updated_at: string
}

export interface OperationBatchConfirmationReceipt {
  api_version: 'setpoint.io/v1'
  kind: 'OperationBatchConfirmation'
  batch_id: string
  source_check_run_id: string
  confirmation_fingerprint: string
  confirmation_idempotency_key: string
  accepted_at: string
  members: OperationBatchConfirmationMember[]
}

export interface OperationBatchConfirmationResponse {
  receipt: OperationBatchConfirmationReceipt
  runs: OperationRun[]
}

export interface OperationRun {
  api_version: 'setpoint.io/v1'
  kind: 'OperationRun'
  metadata: { id: string; idempotency_key: string; created_at: string }
  spec: {
    operation_id: string
    operation_version: string
    capability_digest: string
    node_id: string
    targets: OperationTarget[]
    parameters: Record<string, unknown>
    secret_refs?: SecretRef[]
  }
  status: {
    state: OperationState
    checkpoint: string
    task_id: string
    updated_at: string
    apply_available: boolean
    block?: { code: string; message: string; safe_next_action: string; manual_review: boolean }
    recovery?: { code: string; checkpoint?: string; safe_next_action: string; manual_review: boolean }
  }
  discovery?: {
    applicable: boolean
    summary: string
    targets: OperationTarget[]
    snapshot: OperationArtifact
    findings?: OperationFinding[]
  }
  precheck?: {
    passed: boolean
    summary: string
    snapshot: OperationArtifact
    findings?: OperationFinding[]
  }
  plan?: {
    schema_version: string
    summary: string
    steps: Array<{
      id: string
      name: string
      target: OperationTarget
      action: string
      checkpoint: string
      writes: boolean
      retry_safe: boolean
      rollback_action?: string
    }>
    execution: OperationArtifact
    findings?: OperationFinding[]
  }
  impact?: {
    summary: string
    risk: OperationRisk
    changes: Array<{ target: OperationTarget; before: string; after: string; risk: string }>
    requires_downtime: boolean
    requires_write_fence: boolean
    estimated_duration: number
    estimated_data_change_bytes: number
  }
  plan_digest?: string
  execution?: {
    restore_point?: OperationRestorePoint
    apply?: OperationApplyResult
    verification?: OperationVerification
    rollback?: OperationRollbackResult
    rollback_verification?: OperationVerification
  }
}
