import { t } from '@/i18n/messages';
import type { OperatorSessionStatusReason } from '@/types/operator';

const reasonLabels: Record<OperatorSessionStatusReason, string> = {
  no_session: t('operator.reason.noSession'),
  heartbeat_timeout: t('operator.reason.heartbeatTimeout'),
  heartbeat_delayed: t('operator.reason.heartbeatDelayed'),
  certificate_revoked: t('operator.reason.certificateRevoked'),
  operator_superseded: t('operator.reason.superseded'),
  session_replaced: t('operator.reason.sessionReplaced'),
  unknown: t('operator.reason.unknown'),
};

export function operatorSessionReasonLabel(reason: OperatorSessionStatusReason | string | null): string | null {
  if (!reason) return null;
  // hasOwnProperty: a reason like 'constructor' must not reach Object.prototype.
  const key = reason as OperatorSessionStatusReason;
  if (Object.prototype.hasOwnProperty.call(reasonLabels, key)) return reasonLabels[key];
  return t('operator.reason.unknownWith', { reason });
}

export function formatOperatorTime(value: string | null): string {
  if (!value) return t('operator.time.never');
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value));
}
