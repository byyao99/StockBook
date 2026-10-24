import { describe, expect, it } from 'vitest'
import { DEFAULT_HOLDING_SORT, nextSort, sortHoldings } from './holdingSort'
import { fromShares } from './shares'
import type { Position } from './types'

function holding(symbol: string, overrides: Partial<Position> = {}): Position {
  return {
    id: symbol,
    instrument_id: symbol,
    symbol,
    name: symbol,
    market: 'NASDAQ',
    asset_type: 'EQUITY',
    currency: 'USD',
    quantity: fromShares(1),
    cost_basis: 10_000,
    realized_pl: 0,
    last_price: 10_000,
    price_updated_at: '2026-10-23T00:00:00Z',
    market_value: 10_000,
    unrealized_pl: 0,
    updated_at: '2026-10-23T00:00:00Z',
    ...overrides,
  }
}

const symbols = (rows: Position[]) => rows.map((p) => p.symbol)
const noWeight = () => null

describe('sortHoldings', () => {
  it('sorts by symbol by default', () => {
    const rows = [holding('PLTR'), holding('AMD'), holding('NVDA')]
    expect(symbols(sortHoldings(rows, DEFAULT_HOLDING_SORT, noWeight))).toEqual(['AMD', 'NVDA', 'PLTR'])
  })

  it('sorts an amount largest first when descending', () => {
    const rows = [
      holding('AMD', { market_value: 60_000 }),
      holding('PLTR', { market_value: 180_000 }),
      holding('INTC', { market_value: 50_000 }),
    ]
    const sorted = sortHoldings(rows, { key: 'market_value', dir: 'desc' }, noWeight)
    expect(symbols(sorted)).toEqual(['PLTR', 'AMD', 'INTC'])
  })

  // An unquoted holding has no market value. As 0 it would sit at the bottom of
  // "largest first" and the top of "smallest first"; it belongs last in both.
  it('puts unknown values last in either direction', () => {
    const rows = [
      holding('AMD', { market_value: 60_000 }),
      holding('XYZ', { market_value: null, last_price: null }),
      holding('PLTR', { market_value: 180_000 }),
    ]
    expect(symbols(sortHoldings(rows, { key: 'market_value', dir: 'desc' }, noWeight))).toEqual([
      'PLTR',
      'AMD',
      'XYZ',
    ])
    expect(symbols(sortHoldings(rows, { key: 'market_value', dir: 'asc' }, noWeight))).toEqual([
      'AMD',
      'PLTR',
      'XYZ',
    ])
  })

  // There is no FX rate, so NT$3,000,000 is not "more" than $50,000.
  it('never compares amounts across currencies', () => {
    const rows = [
      holding('VOO', { market_value: 5_000_000 }),
      holding('2330', { currency: 'TWD', market_value: 300_000_000 }),
      holding('AMD', { market_value: 6_000_000 }),
      holding('0050', { currency: 'TWD', market_value: 10_000_000 }),
    ]
    const sorted = sortHoldings(rows, { key: 'market_value', dir: 'desc' }, noWeight)
    expect(symbols(sorted)).toEqual(['2330', '0050', 'AMD', 'VOO'])
  })

  // A weight is a share of its own currency's book, so it does compare across.
  it('compares weights across currencies', () => {
    const weights: Record<string, number> = { VOO: 0.2, '2330': 0.9, AMD: 0.5 }
    const rows = [holding('VOO'), holding('2330', { currency: 'TWD' }), holding('AMD')]
    const sorted = sortHoldings(rows, { key: 'weight', dir: 'desc' }, (p) => weights[p.symbol])
    expect(symbols(sorted)).toEqual(['2330', 'AMD', 'VOO'])
  })

  it('sorts by return, which is measured rather than stored', () => {
    // One share each at a $100 cost, so the quote alone sets the return.
    const rows = [
      holding('AMD', { last_price: 15_000 }), // +50%
      holding('INTC', { last_price: 8_000 }), // −20%
      holding('PLTR', { last_price: 11_000 }), // +10%
    ]
    const sorted = sortHoldings(rows, { key: 'return', dir: 'desc' }, noWeight)
    expect(symbols(sorted)).toEqual(['AMD', 'PLTR', 'INTC'])
  })

  it('breaks ties by symbol so the order is stable', () => {
    const rows = [holding('VOO'), holding('AMD'), holding('MAGS')]
    const sorted = sortHoldings(rows, { key: 'realized', dir: 'desc' }, noWeight)
    expect(symbols(sorted)).toEqual(['AMD', 'MAGS', 'VOO'])
  })

  it('does not reorder the array it was given', () => {
    const rows = [holding('PLTR'), holding('AMD')]
    sortHoldings(rows, DEFAULT_HOLDING_SORT, noWeight)
    expect(symbols(rows)).toEqual(['PLTR', 'AMD'])
  })
})

describe('nextSort', () => {
  it('starts an amount column largest first and symbol A to Z', () => {
    expect(nextSort(DEFAULT_HOLDING_SORT, 'market_value')).toEqual({ key: 'market_value', dir: 'desc' })
    expect(nextSort({ key: 'weight', dir: 'desc' }, 'symbol')).toEqual({ key: 'symbol', dir: 'asc' })
  })

  it('flips the direction when the same column is clicked again', () => {
    expect(nextSort({ key: 'weight', dir: 'desc' }, 'weight')).toEqual({ key: 'weight', dir: 'asc' })
  })
})
