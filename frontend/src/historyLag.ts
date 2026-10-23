/**
 * How far the stored price history lags the period it is meant to cover.
 *
 * Every figure on /reports and a holding's chart is valued at stored daily
 * closes, while Holdings values at the live quote. When the closes fall behind
 * — nobody pressed Sync for a week — the two pages disagree, and a lagging
 * curve looks like any other: a book that was at $5,327 on the last stored
 * session and $5,716 live read as a −15% year on one page and +49.6% on the
 * other, with only a small "2026-10-16 · latest" to say why.
 *
 * The lag is measured to whichever comes first, the period's end or today. A
 * report on 2024 ending in December 2024 is complete, not stale, and must not
 * prompt a sync or a warning.
 */

/**
 * Calendar days a curve may trail before it counts as behind. A weekend plus
 * the overnight gap between a US close and a Taipei morning makes three days
 * ordinary; a fourth means sessions are missing.
 */
export const STALE_AFTER_DAYS = 3

const DAY_MS = 24 * 60 * 60 * 1000

function dayNumber(date: string): number {
  return Date.parse(`${date}T00:00:00Z`) / DAY_MS
}

/**
 * Whole days from the last stored session to the end of what should be
 * covered, or null when there is nothing to measure (no sessions). Dates are
 * YYYY-MM-DD in UTC, like every calendar day in this system; `windowEnd` empty
 * means open-ended.
 */
export function historyLag(
  lastSession: string | undefined,
  windowEnd: string,
  today: string,
): number | null {
  if (!lastSession) return null
  const reference = windowEnd && windowEnd < today ? windowEnd : today
  return Math.max(0, Math.round(dayNumber(reference) - dayNumber(lastSession)))
}

/** Whether a lag is large enough to sync for and to warn about. */
export function isStale(lag: number | null): boolean {
  return lag !== null && lag > STALE_AFTER_DAYS
}
