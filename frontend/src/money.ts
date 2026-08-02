// Money helpers. The API speaks integer minor units (hundredths); the UI shows
// and edits whole units.
//
// Every formatter takes an optional currency. Amounts in different currencies
// are never added or converted anywhere in this app, so the symbol is purely a
// label — but an unlabelled one would be a lie the moment a book holds both.
import type { Currency } from './types'

const SYMBOLS: Record<Currency, string> = { TWD: 'NT$', USD: '$' }

/** The prefix for a currency, defaulting to a bare "$" when none is given. */
export function currencySymbol(currency?: Currency): string {
  return currency ? SYMBOLS[currency] : '$'
}

/** Format integer minor units, e.g. (18000, 'USD') -> "$180.00". */
export function formatCents(cents: number, currency?: Currency): string {
  return `${currencySymbol(currency)}${(cents / 100).toFixed(2)}`
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
export function formatSignedCents(cents: number, currency?: Currency): string {
  const sign = cents < 0 ? '-' : '+'
  return `${sign}${formatCents(Math.abs(cents), currency)}`
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
export function formatCentsOrUnknown(cents: number | null, currency?: Currency): string {
  return cents === null ? UNKNOWN : formatCents(cents, currency)
}

/** Format a possibly-null profit or loss, falling back to the unknown marker. */
export function formatSignedOrUnknown(cents: number | null, currency?: Currency): string {
  return cents === null ? UNKNOWN : formatSignedCents(cents, currency)
}

/** Format a possibly-null fraction as a percentage, falling back to the marker. */
export function formatPercentOrUnknown(fraction: number | null): string {
  return fraction === null ? UNKNOWN : formatPercent(fraction)
}

/**
 * Format a rate given in basis points, e.g. 1234 -> "+12.34%". Rates cross the
 * wire as integer basis points for the same reason amounts cross it as integer
 * minor units: the rounding is decided once, on the server, rather than drifting
 * through whatever each client does with 0.12340000000000001.
 */
export function formatBpsOrUnknown(bps: number | null): string {
  return bps === null ? UNKNOWN : formatPercent(bps / 10000)
}
