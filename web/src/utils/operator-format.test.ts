import { describe, expect, it } from 'vitest';
import { operatorSessionReasonLabel } from './operator-format';

describe('operatorSessionReasonLabel', () => {
  it('uses a stable fallback for unknown future reasons without reclassifying status', () => {
    expect(operatorSessionReasonLabel('future_reason')).toBe(
      'The service reported an unknown status reason (future_reason).',
    );
  });
});
