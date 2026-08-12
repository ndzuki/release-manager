import { describe, expect, it } from 'vitest';
import { validateEnrollmentForm, validateRevokeReason } from './operator-validation';

describe('operator validation', () => {
  it.each([0, 5, 1440])('accepts TTL boundary %s', (ttlMinutes) => {
    expect(validateEnrollmentForm({ operatorName: 'operator-1', ttlMinutes }).valid).toBe(true);
  });

  it.each([4, 1441])('rejects TTL boundary %s', (ttlMinutes) => {
    expect(validateEnrollmentForm({ operatorName: 'operator-1', ttlMinutes }).violations)
      .toContainEqual(expect.objectContaining({ field: 'ttlMinutes' }));
  });

  it.each(['', '-operator', 'operator-', 'Operator', 'operator_name', 'a'.repeat(64)])(
    'rejects invalid DNS label %s',
    (operatorName) => {
      expect(validateEnrollmentForm({ operatorName, ttlMinutes: 60 }).violations)
        .toContainEqual(expect.objectContaining({ field: 'operatorName' }));
    },
  );

  it('counts Unicode code points for revoke reason boundaries', () => {
    expect(validateRevokeReason('安全事件原因')).toBeNull();
    expect(validateRevokeReason('四个字')).toMatchObject({ field: 'reason' });
    expect(validateRevokeReason('界'.repeat(501))).toMatchObject({ field: 'reason' });
  });
});
