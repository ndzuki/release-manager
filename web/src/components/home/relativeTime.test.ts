import { describe, expect, it } from 'vitest';
import { relativeTime } from './relativeTime';

/*
 * The overview shows "3 分钟前" per row. The thresholds are the contract: below a
 * minute is "刚刚" (not "0 分钟前"), the unit steps up exactly at its boundary, and
 * anything older than 30 days falls back to the absolute console format so a stale
 * row still carries the calendar day.
 */
const now = new Date('2026-10-20T12:00:00.000Z').getTime();

function minutesAgo(minutes: number): string {
  return new Date(now - minutes * 60_000).toISOString();
}

describe('relativeTime', () => {
  it('renders sub-minute values as just now', () => {
    expect(relativeTime(minutesAgo(0), now)).toBe('刚刚');
    expect(relativeTime(new Date(now - 30_000).toISOString(), now)).toBe('刚刚');
  });

  it('steps through minutes, hours and days at their boundaries', () => {
    expect(relativeTime(minutesAgo(1), now)).toBe('1 分钟前');
    expect(relativeTime(minutesAgo(59), now)).toBe('59 分钟前');
    expect(relativeTime(minutesAgo(60), now)).toBe('1 小时前');
    expect(relativeTime(minutesAgo(23 * 60), now)).toBe('23 小时前');
    expect(relativeTime(minutesAgo(24 * 60), now)).toBe('1 天前');
    expect(relativeTime(minutesAgo(29 * 24 * 60), now)).toBe('29 天前');
  });

  it('falls back to the absolute format past 30 days', () => {
    const rendered = relativeTime(minutesAgo(31 * 24 * 60), now);

    expect(rendered).not.toContain('天前');
    expect(rendered).toContain('2026');
  });

  // A clock that ran backwards must not produce "-3 分钟前".
  it('clamps a future timestamp to just now', () => {
    expect(relativeTime(new Date(now + 60_000).toISOString(), now)).toBe('刚刚');
  });

  it('labels a missing or unparseable value instead of rendering a dash', () => {
    expect(relativeTime(null, now)).toBe('时间未知');
    expect(relativeTime(undefined, now)).toBe('时间未知');
    expect(relativeTime('not-a-timestamp', now)).toBe('时间未知');
  });
});
