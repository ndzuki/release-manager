import { describe, expect, it } from 'vitest';
import { formatOperatorTime, operatorSessionReasonLabel } from './operator-format';

describe('operatorSessionReasonLabel', () => {
  it('uses a stable fallback for unknown future reasons without reclassifying status', () => {
    // The reason code stays in the message: an operator must still be able to quote it.
    expect(operatorSessionReasonLabel('future_reason')).toBe(
      '服务端上报了未知的状态原因（future_reason）。',
    );
  });
});

describe('prototype safety and time formatting', () => {
  // `reasonLabels['constructor']` used to return Object's constructor through the
  // prototype chain instead of falling back to the catalog.
  it('does not leak Object.prototype members as reason labels', () => {
    expect(operatorSessionReasonLabel('constructor')).toBe('服务端上报了未知的状态原因（constructor）。');
    expect(operatorSessionReasonLabel('toString')).not.toContain('[native code]');
  });

  it('labels a missing timestamp in Chinese', () => {
    expect(formatOperatorTime(null)).toBe('从未');
  });
});
