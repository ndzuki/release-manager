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
      description: 'Operator name must be a lowercase DNS-compatible label with 1 to 63 characters.',
    });
  }
  if (input.ttlMinutes !== 0 && (input.ttlMinutes < 5 || input.ttlMinutes > 1440)) {
    violations.push({ field: 'ttlMinutes', description: 'TTL must be 0 or between 5 and 1440 minutes.' });
  }
  return { valid: violations.length === 0, violations };
}

export function validateRevokeReason(reason: string): OperatorFieldViolation | null {
  const length = [...reason.trim()].length;
  if (length < 5 || length > 500) {
    return { field: 'reason', description: 'Revocation reason must contain 5 to 500 characters.' };
  }
  return null;
}
