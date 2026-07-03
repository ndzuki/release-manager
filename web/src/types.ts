// release-manager 前端类型定义

export interface Customer {
  id: string
  name: string
  operator_endpoint: string
  cert_fingerprint: string
  enabled: boolean
  labels?: Record<string, string>
  created_at: string
  updated_at: string
}

export interface ChartDefinition {
  id: string
  org_id: string
  name: string
  description: string
  oci_url: string
  default_values?: Record<string, unknown>
  labels?: Record<string, string>
  enabled: boolean
  created_at: string
}

export interface CustomerChartBinding {
  id: string
  org_id: string
  customer_id: string
  chart_id: string
  chart_name: string
  enabled: boolean
  release_name: string
  namespace: string
  custom_values?: Record<string, unknown>
  deploy_order: number
  current_version?: string
  last_deployed_at?: string
  last_status?: string
}

export interface ReleaseRecord {
  id: string
  request_id: string
  customer_id: string
  chart_name: string
  chart_version: string
  status: string
  error_message: string
  duration_secs: number
  started_at: string
  completed_at: string
}

export interface CustomerStatus {
  customer_id: string
  customer_name: string
  online: boolean
  last_seen_at: string
  release_count: number
  failed_releases: number
  days_until_cert_expiry: number
}

export interface CertificateInfo {
  fingerprint: string
  subject: string
  not_after: string
  days_until_expiry: number
  issuer: string
}

export interface SystemOverview {
  total_customers: number
  enabled_customers: number
  total_charts: number
  total_deployments: number
  release_success_rate: number
  recent_releases: ReleaseRecord[]
  customer_statuses: CustomerStatus[]
  certificate_warnings: CertificateWarning[]
}

export interface CertificateWarning {
  customer_id: string
  customer_name: string
  days_until_expiry: number
  fingerprint: string
  severity: 'warning' | 'critical'
}

export interface User {
  id: string
  org_id: string
  name: string
  email: string
  role: 'admin' | 'operator' | 'viewer'
  auth_provider: string
}
