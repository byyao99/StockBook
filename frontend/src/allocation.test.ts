import { describe, expect, it } from 'vitest'
import { allocateBy, assetTypeLabel, byAssetType, byHolding, byMarket, topWeight } from './allocation'
import { fromShares } from './shares'
import type { Position } from './types'

// holding builds a position with only the fields allocation reads set from the
// arguments; the rest are plausible defaults.
function holding(overrides: Partial<Position> = {}): Position {
  return {
    id: 'p1',
    instrument_id: 'i1',
    symbol: '2330',
    name: 'TSMC',
    market: 'TWSE',
    asset_type: 'EQUITY',
    currency: 'TWD',
    quantity: fromShares(100),
    cost_basis: 900_000,
    realized_pl: 0,
    last_price: 100_000,
    price_updated_at: '2026-07-25T00:00:00Z',
    market_value: null,
    unrealized_pl: null,
    updated_at: '2026-07-25T00:00:00Z',
    ...overrides,
  }
}

describe('byHolding', () => {
  it('weights each holding against the priced total, largest first', () => {
    const got = byHolding([
      holding({ instrument_id: 'a', symbol: 'A', quantity: fromShares(100), last_price: 10_000 }),
      holding({ instrument_id: 'b', symbol: 'B', quantity: fromShares(100), last_price: 30_000 }),
    ])

    expect(got.total).toBe(4_000_000)
    expect(got.slices.map((s) => s.label)).toEqual(['B', 'A'])
    expect(got.slices[0].weight).toBeCloseTo(0.75)
    expect(got.slices[1].weight).toBeCloseTo(0.25)
  })

  // The whole reason this file exists: "am I 60% in one stock?" has to be
  // answerable exactly.
  it('reports a concentrated book as concentrated', () => {
    const got = byHolding([
      holding({ instrument_id: 'a', symbol: 'A', quantity: fromShares(100), last_price: 60_000 }),
      holding({ instrument_id: 'b', symbol: 'B', quantity: fromShares(100), last_price: 20_000 }),
      holding({ instrument_id: 'c', symbol: 'C', quantity: fromShares(100), last_price: 20_000 }),
    ])
    expect(got.slices[0].weight).toBeCloseTo(0.6)
    expect(topWeight(got, 2)).toBeCloseTo(0.8)
  })

  // An unpriced holding may be the largest position in the book. Counting it
  // as a 0% slice would claim it is negligible, and — worse — would leave every
  // other weight looking like the whole picture.
  it('excludes an unpriced holding and counts it instead', () => {
    const got = byHolding([
      holding({ instrument_id: 'a', symbol: 'A', quantity: fromShares(100), last_price: 10_000 }),
      holding({ instrument_id: 'b', symbol: 'B', quantity: fromShares(100), last_price: null }),
    ])

    expect(got.slices).toHaveLength(1)
    expect(got.unpriced).toBe(1)
    // The one priced holding is all of what *can* be seen, and says so.
    expect(got.slices[0].weight).toBeCloseTo(1)
  })

  // A closed holding holds no shares, so it is no part of how the book is
  // divided today however large it once was.
  it('drops closed holdings', () => {
    const got = byHolding([
      holding({ instrument_id: 'a', symbol: 'A', quantity: fromShares(100), last_price: 10_000 }),
      holding({ instrument_id: 'b', symbol: 'B', quantity: 0, last_price: 10_000 }),
    ])
    expect(got.slices).toHaveLength(1)
    expect(got.unpriced).toBe(0)
  })

  it('has nothing to report for an empty book', () => {
    const got = byHolding([])
    expect(got.slices).toEqual([])
    expect(got.total).toBe(0)
    expect(topWeight(got, 3)).toBeNull()
  })
})

describe('grouping', () => {
  it('sums several holdings into one market slice', () => {
    const got = byMarket([
      holding({ instrument_id: 'a', market: 'TWSE', quantity: fromShares(100), last_price: 10_000 }),
      holding({ instrument_id: 'b', market: 'TWSE', quantity: fromShares(100), last_price: 10_000 }),
      holding({ instrument_id: 'c', market: 'TPEX', quantity: fromShares(100), last_price: 20_000 }),
    ])

    expect(got.slices).toHaveLength(2)
    const twse = got.slices.find((s) => s.key === 'TWSE')
    expect(twse?.positions).toBe(2)
    expect(twse?.weight).toBeCloseTo(0.5)
  })

  // An instrument the provider never classified is its own answer. Folding it
  // into stocks would make the split look decided when it is not.
  it('keeps an unclassified instrument out of both buckets', () => {
    const got = byAssetType([
      holding({ instrument_id: 'a', asset_type: 'ETF', quantity: fromShares(100), last_price: 10_000 }),
      holding({ instrument_id: 'b', asset_type: '', quantity: fromShares(100), last_price: 10_000 }),
    ])

    expect(got.slices.map((s) => s.label).sort()).toEqual(['ETFs', 'Unclassified'])
  })

  it('names the provider codes in the reader\'s words', () => {
    expect(assetTypeLabel('EQUITY')).toBe('Stocks')
    expect(assetTypeLabel('etf')).toBe('ETFs')
    expect(assetTypeLabel('')).toBe('Unclassified')
    // Anything the provider invents next is passed through rather than hidden.
    expect(assetTypeLabel('MUTUALFUND')).toBe('MUTUALFUND')
  })

  it('breaks ties on the label so the order does not wander', () => {
    const got = allocateBy(
      [
        holding({ instrument_id: 'a', symbol: 'Z', quantity: fromShares(100), last_price: 10_000 }),
        holding({ instrument_id: 'b', symbol: 'A', quantity: fromShares(100), last_price: 10_000 }),
      ],
      (p) => p.instrument_id,
      (p) => p.symbol,
    )
    expect(got.slices.map((s) => s.label)).toEqual(['A', 'Z'])
  })
})
