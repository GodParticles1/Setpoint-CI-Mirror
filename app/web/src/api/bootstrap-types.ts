export interface NodeBootstrapInstallProfile {
  mode: 'root' | 'non-root'
  root: string
  binary_path: string
  config_path: string
  identity_path: string
  credential_path: string
  task_journal_path: string
  enrollment_token_path: string
}

export interface NodeBootstrapGatewayInput {
  address: string
  port: number
  username: string
  password: string
}

export interface NodeBootstrapProbeResponse {
  host_key_fingerprint: string
  gateway_host_key_fingerprint?: string
  os: string
  os_version: string
  arch: string
  username: string
  uid: number
  mode: 'root' | 'non-root'
  home: string
  agent_present: boolean
  target_install_profile: NodeBootstrapInstallProfile
}

export interface NodeBootstrapApplyResponse {
  node_id: string
  hostname: string
  os: string
  os_version: string
  arch: string
  agent_version: string
  status: 'online'
  site_id?: string
}
