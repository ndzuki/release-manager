import { t } from '@/i18n/messages';
import type { EnrollmentFormInput, OperatorFieldViolation } from '@/types/operator';

export interface OperatorValidationResult {
  valid: boolean;
  violations: OperatorFieldViolation[];
}

export function validateEnrollmentForm(input: EnrollmentFormInput): OperatorValidationResult {
  const violations: OperatorFieldViolation[] = [];
  const name = input.operatorName.trim();
  if (!/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(name)) {
    violations.push({
      field: 'operatorName',
      description: t('operator.validation.name'),
    });
  }
  if (input.ttlMinutes !== 0 && (input.ttlMinutes < 5 || input.ttlMinutes > 1440)) {
    violations.push({ field: 'ttlMinutes', description: t('operator.validation.ttl') });
  }
  return { valid: violations.length === 0, violations };
}

export function validateRevokeReason(reason: string): OperatorFieldViolation | null {
  const length = [...reason.trim()].length;
  if (length < 5 || length > 500) {
    return { field: 'reason', description: t('operator.validation.reason') };
  }
  return null;
}
