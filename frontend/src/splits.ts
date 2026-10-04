/**
 * Reading a split ratio, and saying what it did to a recorded share count.
 *
 * A split is the one market event this system stores and deliberately does not
 * model: correcting a book for one means restating every share count recorded
 * before it, which is a different project (see CLAUDE.md). So what these
 * functions are for is telling a reader exactly what is wrong and by how much —
 * which is the most this app can honestly offer, and far more than the silence
 * it replaces.
 *
 * `ratio_ppm` is how many shares one share became, in millionths, because the
 * ratio is often not a whole number: a Taiwanese stock dividend of NT$0.8 a
 * share turns 100 shares into 108.
 */
import { formatShares } from './shares'

/** How many stored units make a ratio of one; mirrors models.SplitRatioScale. */
export const SPLIT_RATIO_SCALE = 1_000_000

/**
 * A ratio as a reader would say it: "2:1", "1.08:1", "1:5".
 *
 * A **reverse** split is printed the other way round — 200_000 ppm is one share
 * for every five, and "0.2:1" is a true statement that nobody says out loud.
 * Getting this backwards would understate a 1-for-5 consolidation as a rounding
 * error, so the direction is decided by the ratio rather than left to the
 * reader.
 *
 * Fractions are printed to at most four decimal places and never with trailing
 * zeros, so an exact 2:1 does not render as "2.0000:1".
 */
export function formatSplitRatio(ratioPpm: number): string {
  const ratio = ratioPpm / SPLIT_RATIO_SCALE
  if (ratio <= 0) return '—'
  if (ratio >= 1) return `${trim(ratio)}:1`
  return `1:${trim(1 / ratio)}`
}

/**
 * What a recorded, pre-split share count became.
 *
 * This is the number the warning exists to give: the ledger says 1,000 shares
 * and the prices say they are worth half as much each, so the entries behind it
 * describe 2,000 shares of today's stock. It is the correction a reader would
 * have to make by hand, stated rather than applied.
 */
export function sharesAfterSplit(units: number, ratioPpm: number): number {
  return Math.round((units * ratioPpm) / SPLIT_RATIO_SCALE)
}

/** The same, formatted — the share count a reader recognizes. */
export function formatSharesAfterSplit(units: number, ratioPpm: number): string {
  return formatShares(sharesAfterSplit(units, ratioPpm))
}

/** Drops trailing zeros from a fixed-point number: 2.0000 becomes 2. */
function trim(value: number): string {
  return String(Number(value.toFixed(4)))
}
