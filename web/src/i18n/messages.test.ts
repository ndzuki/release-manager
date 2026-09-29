import { describe, expect, it, vi } from 'vitest';
import { hasMessage, interpolate, messageKeys, t } from './messages';

describe('message catalog', () => {
  it('interpolates named parameters and leaves unknown placeholders alone', () => {
    expect(interpolate('已移除成员 {userId}', { userId: 'u-1' })).toBe('已移除成员 u-1');
    expect(interpolate('{known} 与 {unknown}', { known: 'a' })).toBe('a 与 {unknown}');
    expect(interpolate('无参数')).toBe('无参数');
    expect(t('nav.customers')).toBe('客户');
    expect(hasMessage('nav.customers')).toBe(true);
  });

  // 'constructor' / 'toString' resolve through the prototype for a plain lookup.
  it.each(['constructor', 'toString', '__proto__'])('does not resolve %s from the prototype', (key) => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});

    expect(t(key as never)).toBe(key);
    expect(hasMessage(key)).toBe(false);
    warn.mockRestore();
  });

  it('knows every key it exports', () => {
    for (const key of messageKeys()) {
      expect(hasMessage(key)).toBe(true);
      expect(t(key)).not.toBe('');
    }
  });

  // A missing key must be loud, not silently empty.
  it('renders the key itself for an unknown message', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const rendered = t('action.doesNotExist' as never);

    expect(rendered).toBe('action.doesNotExist');
    expect(warn).toHaveBeenCalledTimes(1);
    // …and only once per key, so a render loop cannot flood the console.
    expect(t('action.doesNotExist' as never)).toBe('action.doesNotExist');
    expect(warn).toHaveBeenCalledTimes(1);
    warn.mockRestore();
  });

  it('keeps reason.* keys verbatim so they can match server codes', () => {
    const reasonKeys = messageKeys().filter((key) => key.startsWith('reason.'));
    expect(reasonKeys.length).toBeGreaterThan(10);
    for (const key of reasonKeys) {
      const code = key.slice('reason.'.length);
      // Server reason codes are lower_snake_case, except the two legacy uppercase ones.
      expect(code).toMatch(/^([a-z0-9_]+|[A-Z0-9_]+)$/);
      expect(code).not.toContain(' ');
    }
  });
});
