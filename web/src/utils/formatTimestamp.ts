/**
 * Single source for the console's timestamp formatting.
 *
 * UX-076/UX-071/UX-077 (W17): thirteen pages and components had each pasted
 * their own `new Intl.DateTimeFormat('zh-CN', …)` call, with three different
 * fallbacks ('—', '未知', '尚未同步', '') and two different time styles (short /
 * medium). The result was the same timestamp rendered two ways on one screen
 * and the same null rendered as "unknown" in one column and "—" in the next.
 * Call sites keep their own fallback word (it is content, not formatting) but
 * the pattern now lives here.
 */
export type TimestampStyle = 'short' | 'medium';

export interface FormatTimestampOptions {
  /** Rendered for null/blank/unparseable input; defaults to an em dash. */
  fallback?: string;
  /** `short` = HH:mm, `medium` = HH:mm:ss. Both use a medium date. */  style?: TimestampStyle;
}

const formatters: Record<TimestampStyle, Intl.DateTimeFormat> = {
  short: new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }),
  medium: new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' }),
};

export function formatTimestamp(
  value: string | number | Date | null | undefined,
  options: FormatTimestampOptions = {},
): string {
  const { fallback = '—', style = 'short' } = options;
  if (value === null || value === undefined || value === '') return fallback;
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return fallback;
  return formatters[style].format(date);
}
