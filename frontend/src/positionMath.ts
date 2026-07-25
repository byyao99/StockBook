// Derived numbers for a holding.
//
// These mirror the backend's own definitions (internal/db/positions.go) and are
// the frontend's contract test surface: every one of them must return null,
// never zero, when the holding cannot be valued. An unknown market value shown
// as $0.00 would read as a total loss.
import type { Position, PortfolioSummary } from './types'

/**
 * Average cost per share, in cents, or null for a closed holding.
 *
 * The backend stores only the exact total cost, never a rounded average, so
 * this is the one place the division happens — at the display edge, where a
 * fractional cent is a formatting question rather than an accounting error.
 * The result is deliberately not rounded; formatCents does that.
 */
export function averageCost(p: Position): number | null {
  if (p.quantity === 0) return null
  return p.cost_basis / p.quantity
}

/** Market value in cents, or null when the instrument has no quote. */
export function marketValue(p: Position): number | null {
  if (p.last_price === null) return null
  return p.quantity * p.last_price
}

/** Unrealized profit/loss in cents, or null when the holding cannot be valued. */
export function unrealizedPL(p: Position): number | null {
  const value = marketValue(p)
  if (value === null) return null
  return value - p.cost_basis
}

/**
 * Unrealized return as a fraction (0.1 === +10%), or null when the holding
 * cannot be valued or has no cost to measure against — a zero cost basis would
 * make the return infinite rather than large.
 */
export function returnPct(p: Position): number | null {
  const pl = unrealizedPL(p)
  if (pl === null || p.cost_basis === 0) return null
  return pl / p.cost_basis
}

/** True when a holding has no quote, so its valuation columns must show as unknown. */
export function isUnpriced(p: Position): boolean {
  return p.last_price === null
}

/**
 * How many open holdings the summary's valuation totals leave out. A non-zero
 * result means `total_market_value` covers only part of the book and the UI
 * should say so rather than presenting it as the whole.
 */
export function unpricedCount(s: PortfolioSummary): number {
  return s.open_positions - s.priced_positions
}

/**
 * Total return on the priced part of the book as a fraction, or null when there
 * is no priced cost to measure against.
 */
export function summaryReturnPct(s: PortfolioSummary): number | null {
  if (s.priced_cost_basis === 0) return null
  return s.total_unrealized_pl / s.priced_cost_basis
}
