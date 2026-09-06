/**
 * How a headline's age is rendered.
 *
 * Deliberately not the reader's locale. A ledger entry's date is displayed by
 * slicing the UTC timestamp precisely so the day a trade belongs to does not
 * depend on where it is read, and a headline follows the same rule: two people
 * looking at one feed should see one date on it.
 *
 * Recent items get a relative age instead, because "3h ago" is what a reader
 * actually wants from a news feed and it carries no zone at all. The cutover is
 * at a day, past which a relative age stops being useful ("19h ago" is fine,
 * "37h ago" is arithmetic the reader has to do).
 */
import { UNKNOWN } from './money'

const MINUTE = 60 * 1000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/**
 * Render a headline's publication time relative to `now`.
 *
 * An unparseable timestamp gives the same em dash every other unknown does,
 * rather than "Invalid Date" or a silent fallback to now — a headline stamped
 * with the moment it was read would sort to the top of the feed forever.
 *
 * A timestamp in the future reads as "just now" rather than as a negative age:
 * a provider's clock running a little ahead of ours is not worth showing a
 * reader, and "-2m ago" would be nonsense.
 */
export function formatNewsTime(published: string, now: Date = new Date()): string {
  const at = Date.parse(published)
  if (Number.isNaN(at)) return UNKNOWN

  const elapsed = now.getTime() - at
  if (elapsed < MINUTE) return 'just now'
  if (elapsed < HOUR) return `${Math.floor(elapsed / MINUTE)}m ago`
  if (elapsed < DAY) return `${Math.floor(elapsed / HOUR)}h ago`
  return utcDate(published)
}

/**
 * The UTC calendar day of a timestamp, as YYYY-MM-DD.
 *
 * Sliced out of the ISO string rather than read off a Date, so it cannot shift
 * by a day for a reader west of Greenwich — the same reason the ledger slices
 * its own dates.
 */
export function utcDate(published: string): string {
  const date = published.slice(0, 10)
  return /^\d{4}-\d{2}-\d{2}$/.test(date) ? date : UNKNOWN
}
