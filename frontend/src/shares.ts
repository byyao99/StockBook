/**
 * Share counts at the UI edge.
 *
 * The API speaks scaled integers for the same reason it speaks integer cents: a
 * float share count would drift, and a position out by 1e-15 of a share
 * compounds through every cost basis built on it. The conversion happens here,
 * once, exactly as `money.ts` converts cents.
 *
 * Fractional shares are real — a broker filling a fixed-amount purchase hands
 * you 2.79 of something — so these are not whole numbers.
 */

/** How many stored units make one share. Mirrors models.SharesScale. */
export const SHARES_SCALE = 1_000_000

/**
 * The most decimals a share count is written with.
 *
 * Six, because that is what the scale carries and what the most precise broker
 * reports. Trailing zeros are trimmed, so a whole holding still reads as "100"
 * rather than "100.000000" — the common case must not be made noisy by the
 * uncommon one.
 */
const MAX_DECIMALS = 6

/** Scaled units to shares, e.g. 2_790_000 -> 2.79. */
export function toShares(units: number): number {
  return units / SHARES_SCALE
}

/**
 * Shares to scaled units, e.g. 2.79 -> 2_790_000.
 *
 * Rounded rather than truncated: 2.79 is not exactly representable as a float,
 * so the product lands a hair under the integer and truncation would lose the
 * last unit — silently, and differently for different inputs.
 */
export function fromShares(shares: number): number {
  return Math.round(shares * SHARES_SCALE)
}

/**
 * Format a stored share count for display: 100_000_000 -> "100",
 * 2_790_000 -> "2.79".
 *
 * Thousands are grouped, because a holding of ten thousand shares is hard to
 * read otherwise, and the fraction is only shown when there is one.
 *
 * `maxDecimals` exists for figures that are *estimated* rather than recorded. A
 * holding is a fact and deserves every digit it has; a share count worked out
 * from a day's close is a guess, and printing it to six places claims an
 * accuracy it does not have.
 */
export function formatShares(units: number, maxDecimals = MAX_DECIMALS): string {
  return toShares(units).toLocaleString('en-US', {
    minimumFractionDigits: 0,
    maximumFractionDigits: maxDecimals,
  })
}

/** How many decimals an estimated share count is worth writing. */
export const ESTIMATE_DECIMALS = 3
