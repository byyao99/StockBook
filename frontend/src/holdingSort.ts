// Ordering the Holdings table by any of its columns.
//
// Two rules decide every comparison, and both are the rest of the system's:
//
// - **Amounts in different currencies are never compared.** There is no FX rate
//   here, so NT$30,000 against $1,000 is not a question with an answer, and a
//   table sorted by market value across both would interleave them by digit
//   count. Money columns sort by currency first and by amount within it; the
//   columns that are already ratios or counts (weight, return, shares) compare
//   across currencies, since a weight is a share of its own currency's book.
// - **Unknown is not zero.** A holding with no quote has no market value, and
//   sorting it as 0 would put it at the bottom of "largest first" and the top of
//   "smallest first" — exactly where a reader looking for the biggest risk would
//   skip it. Unknowns go last in both directions instead, where their em dash
//   reads as "not ranked" rather than as a rank.
//
// Ties fall back to the symbol, so the order never depends on how the rows
// happened to arrive.
import { averageCost, returnPct } from './positionMath'
import type { Position } from './types'

export type HoldingSortKey =
  | 'symbol'
  | 'shares'
  | 'avg_cost'
  | 'cost_basis'
  | 'last_price'
  | 'market_value'
  | 'weight'
  | 'unrealized'
  | 'return'
  | 'realized'

export type SortDirection = 'asc' | 'desc'

export interface HoldingSort {
  key: HoldingSortKey
  dir: SortDirection
}

export const DEFAULT_HOLDING_SORT: HoldingSort = { key: 'symbol', dir: 'asc' }

/** Columns whose values are amounts of money, and so only compare within a currency. */
const MONEY_KEYS = new Set<HoldingSortKey>([
  'avg_cost',
  'cost_basis',
  'last_price',
  'market_value',
  'unrealized',
  'realized',
])

/**
 * The direction a column sorts in when first chosen. A reader clicking "Market
 * value" wants the largest holding first; one clicking "Symbol" wants A to Z.
 */
export function initialDirection(key: HoldingSortKey): SortDirection {
  return key === 'symbol' ? 'asc' : 'desc'
}

/** The sort after clicking a column: flip it if it is the current one, else start it. */
export function nextSort(current: HoldingSort, key: HoldingSortKey): HoldingSort {
  if (current.key === key) return { key, dir: current.dir === 'asc' ? 'desc' : 'asc' }
  return { key, dir: initialDirection(key) }
}

function valueOf(
  p: Position,
  key: Exclude<HoldingSortKey, 'symbol'>,
  weightOf: (p: Position) => number | null,
): number | null {
  switch (key) {
    case 'shares':
      return p.quantity
    case 'avg_cost':
      return averageCost(p)
    case 'cost_basis':
      return p.cost_basis
    case 'last_price':
      return p.last_price
    case 'market_value':
      return p.market_value
    case 'weight':
      return weightOf(p)
    case 'unrealized':
      return p.unrealized_pl
    case 'return':
      return returnPct(p)
    case 'realized':
      return p.realized_pl
  }
}

/**
 * A sorted copy of the holdings. `weightOf` is passed in because a weight is
 * measured against its currency's summary, which the page holds and this
 * module should not fetch.
 */
export function sortHoldings(
  rows: Position[],
  sort: HoldingSort,
  weightOf: (p: Position) => number | null,
): Position[] {
  const sign = sort.dir === 'asc' ? 1 : -1
  const bySymbol = (a: Position, b: Position) => a.symbol.localeCompare(b.symbol)

  return [...rows].sort((a, b) => {
    if (sort.key === 'symbol') return sign * bySymbol(a, b) || a.currency.localeCompare(b.currency)

    if (MONEY_KEYS.has(sort.key) && a.currency !== b.currency) {
      return a.currency.localeCompare(b.currency)
    }
    const va = valueOf(a, sort.key, weightOf)
    const vb = valueOf(b, sort.key, weightOf)
    if (va === null || vb === null) {
      if (va === vb) return bySymbol(a, b)
      return va === null ? 1 : -1
    }
    return sign * (va - vb) || bySymbol(a, b)
  })
}
