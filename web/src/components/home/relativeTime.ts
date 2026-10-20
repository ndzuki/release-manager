import { t } from '@/i18n/messages';
import { formatTimestamp } from '@/utils/formatTimestamp';

/*
 * Relative time for the overview rows (TASK-394).
 *
 * The design asks for "3 分钟前" next to each operation, which no shared helper
 * provided — thirteen call sites format absolute timestamps through
 * `formatTimestamp`, and an operation list read minutes apart needs the relative
 * form to stay scannable. The thresholds are coarse on purpose (minutes, hours,
 * days) and the absolute format takes over past 30 days, so an old row still says
 * WHICH day instead of "87 天前".
 *
 * A missing or unparseable value returns its own label rather than a dash: "time
 * unknown" and "no value" are different statements, and a row without a creation
 * time is a data problem the reader should see.
 */
export function relativeTime(value: string | null | undefined, now: number = Date.now()): string {
  if (!value) return t('home.time.unknown');
  const time = new Date(value).getTime();
  if (Number.isNaN(time)) return t('home.time.unknown');

  const minutes = Math.floor(Math.max(0, now - time) / 60_000);
  if (minutes < 1) return t('home.time.justNow');
  if (minutes < 60) return t('home.time.minutes', { count: minutes });

  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t('home.time.hours', { count: hours });

  const days = Math.floor(hours / 24);
  if (days < 30) return t('home.time.days', { count: days });

  return formatTimestamp(value);
}
