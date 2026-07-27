import type { OperatorSessionStatusReason } from '@/types/operator';

const reasonLabels: Record<OperatorSessionStatusReason, string> = {
  no_session: 'No session has connected yet.',
  heartbeat_timeout: 'Heartbeat timed out.',
  heartbeat_delayed: 'Heartbeat is delayed.',
  certificate_revoked: 'The operator certificate was revoked.',
  operator_superseded: 'A newer operator identity superseded this one.',
  session_replaced: 'A newer session replaced this connection.',
  unknown: 'The service reported an unknown status reason.',
};

export function operatorSessionReasonLabel(reason: OperatorSessionStatusReason | string | null): string | null {
  if (!reason) return null;
  return reasonLabels[reason as OperatorSessionStatusReason] ?? `The service reported an unknown status reason (${reason}).`;
}

export function formatOperatorTime(value: string | null): string {
  if (!value) return 'Never';
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value));
}
