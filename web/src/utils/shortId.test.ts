import { describe, expect, it } from 'vitest';
import { shortId } from './shortId';

describe('shortId', () => {
  it('keeps short values intact', () => {
    expect(shortId('prod-west')).toBe('prod-west');
    expect(shortId('')).toBe('');
    expect(shortId('   ')).toBe('');
  });

  it('truncates a UUID to the requested head and never drops characters silently', () => {
    const id = '11111111-1111-4111-8111-111111111111';
    expect(shortId(id)).toBe('11111111');
    expect(shortId(id, 8, 4)).toBe('11111111…1111');
    expect(shortId(id).length).toBeLessThan(id.length);
  });

  it('trims before measuring', () => {
    expect(shortId('  prod-west  ')).toBe('prod-west');
  });
});
