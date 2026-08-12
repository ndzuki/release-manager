export type OperatorLifecycleStatus = 'active' | 'superseded' | 'revoked' | 'unknown';
export type OperatorSessionStatus = 'online' | 'suspect' | 'offline' | 'revoked' | 'unknown' | null;
export type OperatorSessionStatusReason =
  | 'no_session'
  | 'heartbeat_timeout'
  | 'heartbeat_delayed'
  | 'certificate_revoked'
  | 'operator_superseded'
  | 'session_replaced'
  | 'unknown';

export interface OperatorSummary {
  id: string;
  name: string;
  customerId: string;
  clusterId: string;
  clusterName: string;
  lifecycleStatus: OperatorLifecycleStatus;
  sessionStatus: OperatorSessionStatus;
  sessionStatusReason: OperatorSessionStatusReason | null;
  lastHeartbeat: string | null;
  registeredAt: string;
  supersededAt: string | null;
  revokedAt: string | null;
}

export interface OperatorDetail extends OperatorSummary {
  supersededBy: string | null;
  revokeReason: string | null;
  instanceId: string | null;
  version: string | null;
  capabilities: Record<string, string>;
}

export interface PendingTokenMetadata {
  state: 'pending' | 'none';
  createdAt: string | null;
  expiresAt: string | null;
  createdByDisplayName: string | null;
}

export interface OperatorListFilters {
  lifecycleStatus: OperatorLifecycleStatus | null;
  sessionStatus: OperatorSessionStatus | 'none';
}

export interface OperatorPage {
  operators: OperatorSummary[];
  nextPageToken: string | null;
  totalCount: number;
  heartbeatIntervalSeconds: number;
}

export interface OperatorDetailResult {
  operator: OperatorDetail;
  heartbeatIntervalSeconds: number;
}

export interface OperatorRevocationResult {
  operator: OperatorSummary;
  changed: boolean;
}

export interface EnrollmentFormInput {
  operatorName: string;
  ttlMinutes: number;
}

export interface EnrollmentTokenMetadata {
  expiresAt: string;
  customerId: string;
  clusterId: string;
  clusterName: string;
  operatorEndpoint: string;
  installCommandTemplateVersion: string;
  installCommandTemplate: string;
}

export interface EnrollmentTokenResult extends EnrollmentTokenMetadata {
  token: string;
}

export interface RevokeFormInput {
  reason: string;
}

export interface OperatorFieldViolation {
  field: string;
  description: string;
}

export interface OperatorApiError {
  code: string;
  message: string;
  fieldViolations?: OperatorFieldViolation[];
  retryable: boolean;
}
