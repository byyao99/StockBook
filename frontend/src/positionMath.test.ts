import { describe, expect, it } from 'vitest'
import {
  averageCost,
  isUnpriced,
  marketValue,
  returnPct,
  summaryReturnPct,
  unpricedCount,
  unrealizedPL,
} from './positionMath'
import type { PortfolioSummary, Position } from './types'

// position builds a holding with sensible defaults for the fields under test.
function position(overrides: Partial<Position> = {}): Position {
  return {
    id: 'p1',
    instrument_id: 'i1',
    symbol: '2330',
    name: 'TSMC',
    market: 'TWSE',
    quantity: 100,
    cost_basis: 900_000, // 100 shares averaging $90.00
    realized_pl: 0,
    last_price: 100_000, // $1000.00
    price_updated_at: '2026-07-25T00:00:00Z',
    market_value: null,
    unrealized_pl: null,
    updated_at: '2026-07-25T00:00:00Z',
    ...overrides,
  }
}

describe('averageCost', () => {
  it('divides the total cost by the shares held', () => {
    expect(averageCost(position({ quantity: 100, cost_basis: 900_000 }))).toBe(9_000)
  })

  it('does not round, so an uneven basis keeps its fractional cent', () => {
    // 4 shares costing 4001 cents average 1000.25 — the backend deliberately
    // never stores this number precisely because rounding it would drift.
    expect(averageCost(position({ quantity: 4, cost_basis: 4001 }))).toBe(1000.25)
  })

  it('is null for a closed holding rather than dividing by zero', () => {
    expect(averageCost(position({ quantity: 0, cost_basis: 0 }))).toBeNull()
  })
})

describe('marketValue', () => {
  it('multiplies the shares held by the quote', () => {
    expect(marketValue(position({ quantity: 100, last_price: 100_000 }))).toBe(10_000_000)
  })

  // The most important rule in this module: an unvalued holding must never
  // render as $0.00, which would read as a total loss.
  it('is null when there is no quote, not zero', () => {
    expect(marketValue(position({ last_price: null }))).toBeNull()
  })
})

describe('unrealizedPL', () => {
  it('is the market value less the cost still tied up', () => {
    expect(unrealizedPL(position({ quantity: 100, cost_basis: 900_000, last_price: 100_000 }))).toBe(
      9_100_000,
    )
  })

  it('goes negative when the quote is below the average cost', () => {
    expect(unrealizedPL(position({ quantity: 10, cost_basis: 100_000, last_price: 5_000 }))).toBe(
      -50_000,
    )
  })

  it('is null without a quote', () => {
    expect(unrealizedPL(position({ last_price: null }))).toBeNull()
  })
})

describe('returnPct', () => {
  it('expresses the gain as a fraction of the cost', () => {
    // 10 shares costing 100,000 now worth 120,000: a 20% gain.
    expect(returnPct(position({ quantity: 10, cost_basis: 100_000, last_price: 12_000 }))).toBeCloseTo(
      0.2,
    )
  })

  it('is null without a quote', () => {
    expect(returnPct(position({ last_price: null }))).toBeNull()
  })

  it('is null rather than infinite when there is no cost to measure against', () => {
    expect(returnPct(position({ quantity: 10, cost_basis: 0, last_price: 5_000 }))).toBeNull()
  })
})

describe('isUnpriced', () => {
  it('identifies the holdings whose valuation columns must read as unknown', () => {
    expect(isUnpriced(position({ last_price: null }))).toBe(true)
    expect(isUnpriced(position({ last_price: 1 }))).toBe(false)
    // A quote of exactly zero is a real quote, not a missing one.
    expect(isUnpriced(position({ last_price: 0 }))).toBe(false)
  })
})

function summary(overrides: Partial<PortfolioSummary> = {}): PortfolioSummary {
  return {
    open_positions: 3,
    priced_positions: 2,
    total_cost_basis: 300_000,
    priced_cost_basis: 200_000,
    total_market_value: 250_000,
    total_unrealized_pl: 50_000,
    total_realized_pl: 10_000,
    ...overrides,
  }
}

describe('unpricedCount', () => {
  it('reports how much of the book the valuation totals leave out', () => {
    expect(unpricedCount(summary())).toBe(1)
    expect(unpricedCount(summary({ open_positions: 2, priced_positions: 2 }))).toBe(0)
  })
})

describe('summaryReturnPct', () => {
  it('measures the gain against the priced cost only', () => {
    // The identity the backend promises: unrealized === market - priced cost.
    const s = summary()
    expect(s.total_unrealized_pl).toBe(s.total_market_value - s.priced_cost_basis)
    expect(summaryReturnPct(s)).toBeCloseTo(0.25)
  })

  it('is null when nothing priced has a cost', () => {
    expect(summaryReturnPct(summary({ priced_cost_basis: 0 }))).toBeNull()
  })
})
