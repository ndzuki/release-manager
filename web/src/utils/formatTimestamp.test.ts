import { describe, expect, it } from 'vitest';
import { formatTimestamp } from './formatTimestamp';

/*
 * W17: one formatter for the whole console. The test pins the two behaviours the
 * thirteen copies disagreed on — the null fallback and the second precision —
 * plus the input shapes the pages actually receive (ISO string from protobuf
 * JSON, epoch millis from local draft records, Date).
 */
const LOCAL = new Date(2026, 9, 17, 20, 2, 46);

describe('formatTimestamp', () => {
  it('renders an em dash for missing or unparseable input by default', () => {
    expect(formatTimestamp(null)).toBe('—');
    expect(formatTimestamp(undefined)).toBe('—');
    expect(formatTimestamp('')).toBe('—');
    expect(formatTimestamp('not-a-date')).toBe('—');
  });

  it('keeps a caller-specific fallback word', () => {
    // ReleaseInventoryTable says "尚未同步"; EmergencyArtifactSelector says "未知".
    expect(formatTimestamp(null, { fallback: '尚未同步' })).toBe('尚未同步');
    expect(formatTimestamp(null, { fallback: '未知' })).toBe('未知');
    expect(formatTimestamp(LOCAL, { fallback: '' })).not.toBe('');
  });

  it('formats a timestamp with minute precision by default', () => {
    const formatted = formatTimestamp(LOCAL.toISOString());
    expect(formatted).toContain('2026');
    expect(formatted).toMatch(/20:02/);
    // timeStyle: 'short' never prints seconds.
    expect(formatted).not.toMatch(/20:02:46/);
  });

  it('adds seconds only when asked (medium)', () => {
    expect(formatTimestamp(LOCAL.toISOString(), { style: 'medium' })).toMatch(/20:02:46/);
  });

  it('accepts epoch millis and Date instances', () => {
    expect(formatTimestamp(LOCAL.getTime())).toBe(formatTimestamp(LOCAL.toISOString()));
    expect(formatTimestamp(LOCAL)).toBe(formatTimestamp(LOCAL.toISOString()));
  });
});
