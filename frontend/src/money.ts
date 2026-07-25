// Money helpers. The API speaks integer cents; the UI shows/edits dollars.

/** Format integer cents as a currency string, e.g. 18000 -> "$180.00". */
export function formatCents(cents: number): string {
  return `$${(cents / 100).toFixed(2)}`
}

/** Convert a dollar amount entered by the user to integer cents. */
export function toCents(dollars: number): number {
  return Math.round(dollars * 100)
}

/** Convert integer cents to a dollar number for editing in a form field. */
export function fromCents(cents: number): number {
  return cents / 100
}

/**
 * Format a profit or loss with an explicit sign, e.g. 2500 -> "+$25.00" and
 * -2500 -> "-$25.00". A gain and a loss must never look alike at a glance, and
 * the sign carries that even where color is unavailable.
 */
export function formatSignedCents(cents: number): string {
  const sign = cents < 0 ? '-' : '+'
  return `${sign}${formatCents(Math.abs(cents))}`
}

/** Format a fraction as a signed percentage, e.g. 0.1234 -> "+12.34%". */
export function formatPercent(fraction: number): string {
  const sign = fraction < 0 ? '-' : '+'
  return `${sign}${(Math.abs(fraction) * 100).toFixed(2)}%`
}

/** Format a share count with thousands separators. */
export function formatQty(qty: number): string {
  return qty.toLocaleString('en-US')
}

/**
 * The placeholder shown wherever a number cannot be computed — an unpriced
 * holding, a closed position's average cost. Rendering these as 0 would be a
 * lie; an em dash says "not known" instead.
 */
export const UNKNOWN = '—'

/** Format a possibly-null cent amount, falling back to the unknown marker. */
export function formatCentsOrUnknown(cents: number | null): string {
  return cents === null ? UNKNOWN : formatCents(cents)
}

/** Format a possibly-null profit or loss, falling back to the unknown marker. */
export function formatSignedOrUnknown(cents: number | null): string {
  return cents === null ? UNKNOWN : formatSignedCents(cents)
}

/** Format a possibly-null fraction as a percentage, falling back to the marker. */
export function formatPercentOrUnknown(fraction: number | null): string {
  return fraction === null ? UNKNOWN : formatPercent(fraction)
}
