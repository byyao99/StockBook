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

/**
 * A short label for a cent amount, e.g. 123456789 -> "NT$1.23M".
 *
 * Only for axis grid labels, which have to fit in the margin beside a plot —
 * "NT$1,234,567.89" does not, and a chart whose y-axis is unreadable is worse
 * than one rounded to three significant figures. The exact amount is never more
 * than a hover away.
 */
export function formatCompactCents(cents: number, currency?: Currency): string {
  const units = cents / 100
  const magnitude = Math.abs(units)
  const sign = units < 0 ? '-' : ''
  const symbol = currencySymbol(currency)
  if (magnitude >= 1e9) return `${sign}${symbol}${(magnitude / 1e9).toFixed(2)}B`
  if (magnitude >= 1e6) return `${sign}${symbol}${(magnitude / 1e6).toFixed(2)}M`
  if (magnitude >= 1e3) return `${sign}${symbol}${(magnitude / 1e3).toFixed(1)}K`
  return `${sign}${symbol}${magnitude.toFixed(0)}`
}

/**
 * Format a basis-point figure that has a size but no direction, e.g. 1599 ->
 * "15.99%".
 *
 * A maximum drawdown is a depth rather than a movement: it is reported as a
 * positive number because it only ever measures a fall. Running it through the
 * signed formatter would print the worst loss a book ever took as "+15.99%",
 * which reads as a gain.
 */
export function formatBpsMagnitudeOrUnknown(bps: number | null): string {
  return bps === null ? UNKNOWN : `${(Math.abs(bps) / 100).toFixed(2)}%`
}

/**
 * Format a rate given in parts per million as a plain percentage, e.g.
 * 1425 -> "0.1425%".
 *
 * A fee rate has a size but no direction, and it needs more precision than the
 * basis-point formatters give: 0.1425% is 14.25 basis points, which is why fee
 * rates cross the wire in ppm at all. Trailing zeros are trimmed so 0.3% does
 * not render as "0.3000%".
 *
 * It accepts a fractional ppm, because an effective rate is a stored rate times
 * a discount and need not land on a whole one: 0.1425% at 33% of list is
 * 470.25 ppm.
 */
export function formatPpmPercent(ppm: number): string {
  const percent = (ppm / 10000).toFixed(6).replace(/\.?0+$/, '')
  return `${percent}%`
}

/**
 * Convert a stored discount in basis points to the percentage of the listed
 * commission actually paid, e.g. 2800 -> 28. A hundred is full price.
 */
export function bpsToListPercent(bps: number): number {
  return bps / 100
}

/** Convert an edited percentage of list back to basis points, e.g. 28 -> 2800. */
export function listPercentToBps(percent: number): number {
  return Math.round(percent * 100)
}

/** Convert parts per million to a percentage for editing, e.g. 1425 -> 0.1425. */
export function ppmToPercent(ppm: number): number {
  return ppm / 10000
}

/** Convert an edited percentage back to parts per million, e.g. 0.1425 -> 1425. */
export function percentToPpm(percent: number): number {
  return Math.round(percent * 10000)
}
