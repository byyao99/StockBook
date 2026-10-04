/**
 * How a currency's book is divided up — by holding, by market, by what kind of
 * thing it is.
 *
 * Every other figure in this app is about profit. This one is about risk, and
 * it is the one question a ledger can answer exactly and a broker statement
 * usually will not: "how much of this is in one stock?"
 *
 * Two rules run through the whole file, and both are the same rule the rest of
 * the system follows.
 *
 * **The denominator is one currency's priced market value.** There is no
 * exchange rate here, so a TWD holding's share of a book that also holds USD is
 * not a number that exists — allocation is computed per currency block, exactly
 * as every total is.
 *
 * **An unpriced holding is excluded, never counted as zero.** Its value is
 * unknown, and a slice of 0% would claim the position is negligible when it may
 * be the largest one in the book. It is left out of the slices and counted
 * separately, so the caller can say what share of the book the picture actually
 * accounts for. A concentration understated is the wrong way for this figure to
 * be wrong.
 */
import { marketValue } from './positionMath'
import type { Position } from './types'

/** One wedge of a currency's book. */
export interface AllocationSlice {
  /** Stable identity for the group — a symbol, a market code, an asset type. */
  key: string
  /** What to print. */
  label: string
  /** Market value in minor units. */
  value: number
  /** Share of the priced total, as a fraction between 0 and 1. */
  weight: number
  /** How many holdings fell in this group. */
  positions: number
}

/** What an allocation covers, and what it had to leave out. */
export interface Allocation {
  slices: AllocationSlice[]
  /** The priced market value the weights are fractions of. */
  total: number
  /** Open holdings with no quote, which are in no slice. */
  unpriced: number
}

/**
 * Slice a currency's holdings by whatever `key` returns, largest first.
 *
 * Closed holdings are dropped: they hold no shares, so they are no part of how
 * the book is currently divided, however much they once were. Ties break on the
 * label so the order does not wander between renders.
 */
export function allocateBy(
  positions: Position[],
  key: (p: Position) => string,
  label: (p: Position) => string = key,
): Allocation {
  const groups = new Map<string, AllocationSlice>()
  let total = 0
  let unpriced = 0

  for (const p of positions) {
    if (p.quantity === 0) continue
    const value = marketValue(p)
    if (value === null) {
      unpriced++
      continue
    }
    total += value
    const k = key(p)
    const slice = groups.get(k)
    if (slice === undefined) {
      groups.set(k, { key: k, label: label(p), value, weight: 0, positions: 1 })
    } else {
      slice.value += value
      slice.positions++
    }
  }

  const slices = [...groups.values()].sort((a, b) =>
    a.value === b.value ? a.label.localeCompare(b.label) : b.value - a.value,
  )
  // Weights are assigned after the totalling rather than during it, because the
  // denominator is not known until every holding has been seen.
  for (const slice of slices) {
    // A book whose priced holdings are all worth nothing has no weights to
    // report. It cannot normally happen — a price of zero is refused on the way
    // in — but dividing by it would hand every slice a NaN.
    slice.weight = total === 0 ? 0 : slice.value / total
  }
  return { slices, total, unpriced }
}

/** One slice per holding: the concentration question in its rawest form. */
export function byHolding(positions: Position[]): Allocation {
  return allocateBy(
    positions,
    (p) => p.instrument_id,
    (p) => p.symbol,
  )
}

/** One slice per market — which exchanges the money sits on. */
export function byMarket(positions: Position[]): Allocation {
  return allocateBy(positions, (p) => p.market)
}

/**
 * One slice per kind of instrument: an ETF-heavy book and a stock-picking one
 * are different books even on the same market.
 *
 * An instrument the provider never classified gets its own bucket rather than
 * being folded into either. Guessing would make the split look decided when it
 * is not, and the fix — a quote refresh, which fills the field in passing — is
 * something the reader can actually do.
 */
export function byAssetType(positions: Position[]): Allocation {
  return allocateBy(
    positions,
    (p) => p.asset_type || 'UNKNOWN',
    (p) => assetTypeLabel(p.asset_type),
  )
}

/** The provider's word for what a listing is, in the reader's. */
export function assetTypeLabel(assetType: string): string {
  switch (assetType.toUpperCase()) {
    case 'EQUITY':
      return 'Stocks'
    case 'ETF':
      return 'ETFs'
    case '':
      return 'Unclassified'
    default:
      return assetType
  }
}

/**
 * The combined weight of the `n` largest slices — "the top three are 62% of
 * this book" — or null when there is nothing priced to measure.
 *
 * Null rather than 0 for the reason a weight is: an unmeasurable concentration
 * is not a low one.
 */
export function topWeight(allocation: Allocation, n: number): number | null {
  if (allocation.slices.length === 0 || allocation.total === 0) return null
  return allocation.slices.slice(0, n).reduce((sum, s) => sum + s.weight, 0)
}
