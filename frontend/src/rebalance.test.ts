import { describe, expect, it } from 'vitest'
import { drift, planContribution, planRebalance, targetsFromCurrent } from './rebalance'
import { fromShares } from './shares'
import type { Position, TargetWeight } from './types'

// A holding of `shares` at `price` cents a share, costing what it is worth.
function holding(symbol: string, shares: number, price: number | null): Position {
  return {
    id: symbol,
    instrument_id: symbol,
    symbol,
    name: symbol,
    market: 'NASDAQ',
    asset_type: 'EQUITY',
    currency: 'USD',
    quantity: fromShares(shares),
    cost_basis: price === null ? 10_000 : shares * price,
    realized_pl: 0,
    last_price: price,
    price_updated_at: null,
    market_value: null,
    unrealized_pl: null,
    updated_at: '2026-10-24T00:00:00Z',
  }
}

function target(symbol: string, percent: number, price: number | null = null): TargetWeight {
  return {
    instrument_id: symbol,
    symbol,
    name: symbol,
    currency: 'USD',
    weight_bps: percent * 100,
    last_price: price,
  }
}

const amounts = (plan: ReturnType<typeof planContribution>) =>
  Object.fromEntries(plan.purchases.map((p) => [p.symbol, p.amount]))

describe('drift', () => {
  it('measures each target against its share of the priced book', () => {
    // $600 VOO, $400 PLTR against 80/20.
    const d = drift([holding('VOO', 6, 10_000), holding('PLTR', 4, 10_000)], [target('VOO', 80), target('PLTR', 20)])
    expect(d.total).toBe(100_000)
    const [voo, pltr] = d.rows
    expect(voo.symbol).toBe('VOO')
    expect(voo.drift).toBeCloseTo(-0.2)
    expect(voo.toTarget).toBe(20_000)
    expect(pltr.drift).toBeCloseTo(0.2)
    expect(pltr.toTarget).toBe(-20_000)
  })

  // Not held is a known zero; held without a quote is unknown.
  it('tells a target not yet held apart from a holding with no quote', () => {
    const d = drift([holding('VOO', 10, 10_000), holding('XYZ', 1, null)], [target('VOO', 60), target('VT', 40)])
    const vt = d.rows.find((r) => r.symbol === 'VT')!
    expect(vt.value).toBe(0)
    expect(vt.weight).toBe(0)
    expect(vt.toTarget).toBe(40_000)

    const xyz = d.rows.find((r) => r.symbol === 'XYZ')!
    expect(xyz.target).toBeNull()
    expect(xyz.value).toBeNull()
    expect(xyz.drift).toBeNull()
    expect(d.unpriced).toBe(1)
    expect(d.total).toBe(100_000)
  })

  // Outside the plan means a target of zero: all of it is overweight.
  it('counts a holding outside the plan as entirely over', () => {
    const d = drift([holding('VOO', 5, 10_000), holding('TSLA', 5, 10_000)], [target('VOO', 100)])
    const tsla = d.rows.find((r) => r.symbol === 'TSLA')!
    expect(tsla.target).toBeNull()
    expect(tsla.drift).toBeCloseTo(0.5)
    expect(tsla.toTarget).toBe(-50_000)
  })
})

describe('planContribution, money mode', () => {
  // Shortfalls against the book after $100 ($1,150): VOO $205, QQQM $65,
  // PLTR −$170. All of it goes to VOO, which is furthest behind; PLTR is over.
  it('funds the largest shortfall first and never the overweight', () => {
    const book = [holding('VOO', 6, 10_000), holding('PLTR', 4, 10_000), holding('QQQM', 0.5, 10_000)]
    const plan = planContribution(book, [target('VOO', 70), target('PLTR', 20), target('QQQM', 10)], 10_000)
    expect(amounts(plan)).toEqual({ VOO: 10_000 })
  })

  // With $200 instead, VOO is brought down to QQQM's $115 gap ($140), and the
  // last $60 is split evenly between the two.
  it('levels the largest shortfalls together once they meet', () => {
    const book = [holding('VOO', 6, 10_000), holding('PLTR', 4, 10_000), holding('QQQM', 0.5, 10_000)]
    const plan = planContribution(book, [target('VOO', 70), target('PLTR', 20), target('QQQM', 10)], 20_000)
    // After $200 the book is $1,250: VOO short $275, QQQM $75. Level D solves
    // (275 − D) + (75 − D) = 200 → D = 75, so VOO takes $200 and QQQM nothing
    // until the gaps meet.
    expect(amounts(plan)).toEqual({ VOO: 20_000 })
    const more = planContribution(book, [target('VOO', 70), target('PLTR', 20), target('QQQM', 10)], 40_000)
    // After $400 ($1,450): VOO short $415, QQQM $95 → D = 55: VOO $360, QQQM $40.
    expect(amounts(more)).toEqual({ VOO: 36_000, QQQM: 4_000 })
  })

  // The case that set the rule: a new 10% target held at zero must not
  // outrank a 35% target that is far more short in money.
  it('does not let an empty small target outrank a large shortfall', () => {
    const book = [holding('VOO', 1, 89_439), holding('PLTR', 10, 19_675), holding('QQQM', 1, 9_675)]
    const plan = planContribution(book, [target('VOO', 50), target('QQQM', 15), target('VT', 10, 13_000), target('PLTR', 25)], 30_000)
    expect(plan.purchases[0].symbol).toBe('VOO')
  })

  it('never pushes a target past its weight', () => {
    const book = [holding('VOO', 9, 10_000), holding('VT', 1, 10_000)]
    const plan = planContribution(book, [target('VOO', 50), target('VT', 50)], 100_000)
    // After $1,000 the book is $2,000: VT short $900, VOO short $100.
    expect(amounts(plan)).toEqual({ VT: 90_000, VOO: 10_000 })
    expect(plan.purchases.every((p) => p.weightAfter <= p.target + 1e-9)).toBe(true)
  })

  it('splits in target proportions once every target is level', () => {
    // Exactly on target: 50/50 at $500 each. $200 more should split 100/100.
    const plan = planContribution([holding('VOO', 5, 10_000), holding('VT', 5, 10_000)], [target('VOO', 50), target('VT', 50)], 20_000)
    expect(amounts(plan)).toEqual({ VOO: 10_000, VT: 10_000 })
    expect(plan.leftover).toBe(0)
  })

  it('always places the exact amount, to the cent', () => {
    const book = [holding('A', 1, 33_333), holding('B', 1, 12_345), holding('C', 2, 7_777)]
    const plan = planContribution(book, [target('A', 33), target('B', 33), target('C', 34)], 100_001)
    const total = plan.purchases.reduce((s, p) => s + p.amount, 0)
    expect(total).toBe(100_001)
    expect(plan.purchases.every((p) => Number.isInteger(p.amount))).toBe(true)
  })

  it('estimates fractional shares at the last price', () => {
    const plan = planContribution([holding('VOO', 1, 70_000)], [target('VOO', 100)], 10_000)
    expect(plan.purchases[0].shares).toBeCloseTo(10_000 / 70_000)
  })

  // A planned purchase of something not yet held is priced from the target.
  it('funds a target not held yet, priced from its own quote', () => {
    const plan = planContribution([holding('VOO', 10, 10_000)], [target('VOO', 50), target('VT', 50, 13_000)], 30_000)
    expect(amounts(plan)).toEqual({ VT: 30_000 })
    expect(plan.purchases[0].shares).toBeCloseTo(30_000 / 13_000)
  })

  it('never funds a holding outside the plan', () => {
    const plan = planContribution([holding('VOO', 1, 10_000), holding('TSLA', 1, 10_000)], [target('VOO', 100)], 5_000)
    expect(amounts(plan)).toEqual({ VOO: 5_000 })
  })

  // Its weight is unknown, so whether it is under or over is too.
  it('skips a target with no quote rather than guessing', () => {
    const plan = planContribution([holding('VOO', 1, 10_000), holding('XYZ', 1, null)], [target('VOO', 50), target('XYZ', 50)], 5_000)
    expect(amounts(plan)).toEqual({ VOO: 5_000 })
    expect(plan.skipped).toEqual([{ symbol: 'XYZ', reason: expect.stringContaining('no quote') }])
  })
})

describe('planContribution, whole-share mode', () => {
  it('buys whole shares of the largest shortfall it can afford, and reports the rest', () => {
    // AVGO is furthest behind but $300 cannot buy a $321.68 share; NVDA can.
    const book = [holding('AVGO', 1, 32_168), holding('NVDA', 1, 21_096), holding('VOO', 3, 69_858)]
    const plan = planContribution(book, [target('AVGO', 30), target('NVDA', 20), target('VOO', 50)], 30_000, {
      wholeShares: true,
    })
    expect(plan.purchases).toEqual([expect.objectContaining({ symbol: 'NVDA', shares: 1, amount: 21_096 })])
    expect(plan.leftover).toBe(30_000 - 21_096)
  })

  it('keeps buying while money remains, alternating as weights move', () => {
    const plan = planContribution([holding('A', 1, 1_000), holding('B', 1, 1_000)], [target('A', 50), target('B', 50)], 4_000, {
      wholeShares: true,
    })
    expect(amounts(plan)).toEqual({ A: 2_000, B: 2_000 })
    expect(plan.leftover).toBe(0)
  })
})

describe('targetsFromCurrent', () => {
  it('starts from the current weights, rounded, adding up to exactly 100%', () => {
    // 1/3 each rounds to 33% three times; the largest takes the missing 1%.
    const got = targetsFromCurrent([holding('A', 1, 10_001), holding('B', 1, 10_000), holding('C', 1, 9_999)])
    expect(got.reduce((s, t) => s + t.weightBps, 0)).toBe(10_000)
    expect(got[0]).toEqual({ instrumentId: 'A', weightBps: 3_400 })
  })

  it('leaves out holdings with no quote', () => {
    const got = targetsFromCurrent([holding('A', 1, 10_000), holding('XYZ', 1, null)])
    expect(got).toEqual([{ instrumentId: 'A', weightBps: 10_000 }])
  })
})

describe('drift, buy only', () => {
  // $600 VOO / $400 PLTR against 80/20. PLTR is the most overweight: without
  // selling, the book has to reach $2,000 for PLTR's $400 to be 20%.
  it('sizes the book by its most overweight line', () => {
    const d = drift([holding('VOO', 6, 10_000), holding('PLTR', 4, 10_000)], [target('VOO', 80), target('PLTR', 20)])
    expect(d.buyOnlyTotal).toBe(100_000)
    expect(d.rows.find((r) => r.symbol === 'VOO')!.toBuy).toBe(100_000)
    expect(d.rows.find((r) => r.symbol === 'PLTR')!.toBuy).toBe(0)
  })

  it('needs nothing when the book is already on target', () => {
    const d = drift([holding('VOO', 5, 10_000), holding('VT', 5, 10_000)], [target('VOO', 50), target('VT', 50)])
    expect(d.buyOnlyTotal).toBe(0)
  })

  // Buying can never bring a holding outside the plan to zero.
  it('leaves holdings outside the plan out of the buy and reports them', () => {
    const d = drift([holding('VOO', 5, 10_000), holding('INTC', 2, 10_000)], [target('VOO', 100)])
    expect(d.buyOnlyTotal).toBe(0)
    expect(d.outside).toBe(20_000)
    expect(d.rows.find((r) => r.symbol === 'INTC')!.toBuy).toBeNull()
  })
})

describe('planRebalance', () => {
  // $600 VOO / $400 PLTR against 80/20, no new money: sell $200 of PLTR, buy
  // $200 of VOO.
  it('sells what is over and buys what is under, landing on target', () => {
    const book = [holding('VOO', 6, 10_000), holding('PLTR', 4, 10_000)]
    const plan = planRebalance(book, [target('VOO', 80), target('PLTR', 20)], 0)
    expect(plan.trades.map((t) => [t.side, t.symbol, t.amount])).toEqual([
      ['sell', 'PLTR', 20_000],
      ['buy', 'VOO', 20_000],
    ])
    expect(plan.cash).toBe(0)
    for (const t of plan.trades) expect(t.weightAfter).toBeCloseTo(t.target)
  })

  it('folds new money into the same trades', () => {
    const book = [holding('VOO', 6, 10_000), holding('PLTR', 4, 10_000)]
    // $1,000 in: the $2,000 book is 1,600 / 400 — no sale needed.
    const plan = planRebalance(book, [target('VOO', 80), target('PLTR', 20)], 100_000)
    expect(plan.trades.map((t) => [t.side, t.symbol, t.amount])).toEqual([['buy', 'VOO', 100_000]])
  })

  // The cost of selling is what it banks, measured as the ledger will.
  it('reports the realized result of each sale at moving-average cost', () => {
    // 10 PLTR bought for $1,686.17 in all, now $196.75: selling 5 removes
    // half the cost ($843.09, rounded half-up) for $983.75 of proceeds.
    const pltr = { ...holding('PLTR', 10, 19_675), cost_basis: 168_617 }
    const plan = planRebalance([pltr, holding('VOO', 1, 196_750)], [target('PLTR', 25), target('VOO', 75)], 0, {
      wholeShares: true,
    })
    const sale = plan.trades.find((t) => t.side === 'sell')!
    expect(sale.symbol).toBe('PLTR')
    expect(sale.shares).toBe(5)
    expect(sale.amount).toBe(98_375)
    expect(sale.realized).toBe(98_375 - 84_309)
    expect(plan.realized).toBe(sale.realized)
  })

  it('sells a holding outside the plan out entirely, cost and all', () => {
    const intc = { ...holding('INTC', 4, 8_696), cost_basis: 35_647 }
    const plan = planRebalance([holding('VOO', 1, 10_000), intc], [target('VOO', 100)], 0)
    const sale = plan.trades.find((t) => t.symbol === 'INTC')!
    expect(sale.shares).toBe(4)
    expect(sale.amount).toBe(34_784)
    expect(sale.realized).toBe(34_784 - 35_647)
  })

  // Rounded toward zero shares, so nothing is overshot; the difference is cash.
  it('reports the cash whole shares leave over or short', () => {
    const book = [holding('VOO', 1, 70_000), holding('PLTR', 4, 20_000)]
    const plan = planRebalance(book, [target('VOO', 50), target('PLTR', 50)], 0, { wholeShares: true })
    // Exact: sell $50 of PLTR, buy $50 of VOO — neither is a whole share.
    expect(plan.trades).toEqual([])
    expect(plan.cash).toBe(0)
  })

  it('spends exactly the contribution in money mode', () => {
    const book = [holding('A', 1, 33_333), holding('B', 1, 12_345), holding('C', 2, 7_777)]
    const plan = planRebalance(book, [target('A', 33), target('B', 33), target('C', 34)], 100_001)
    const net =
      plan.trades.filter((t) => t.side === 'buy').reduce((s, t) => s + t.amount, 0) -
      plan.trades.filter((t) => t.side === 'sell').reduce((s, t) => s + t.amount, 0)
    expect(net).toBe(100_001)
    expect(plan.cash).toBe(0)
  })

  // Rounding drops sales of a fraction of a share but not the buys they were
  // meant to fund. The plan must still be affordable.
  it('never suggests whole-share buys the cash cannot cover', () => {
    const book = [holding('A', 3, 10_000), holding('B', 1, 4_000), holding('C', 1, 30_000)]
    // Exact: sell 0.6 of B ($24) — dropped as under a share — and buy C.
    const plan = planRebalance(book, [target('A', 30), target('B', 10), target('C', 60)], 20_000, {
      wholeShares: true,
    })
    expect(plan.cash).toBeGreaterThanOrEqual(0)
    const net =
      plan.trades.filter((t) => t.side === 'buy').reduce((s, t) => s + t.amount, 0) -
      plan.trades.filter((t) => t.side === 'sell').reduce((s, t) => s + t.amount, 0)
    expect(net + plan.cash).toBe(20_000)
  })

  it('spends what whole-share rounding leaves on the largest shortfall', () => {
    // $1,000 VOO / $100 VT against 50/50 with $100 more: buy $500 of VT
    // exactly, sell $400 of VOO. In $10 shares: sell 40, buy 50.
    const book = [holding('VOO', 100, 1_000), holding('VT', 10, 1_000)]
    const plan = planRebalance(book, [target('VOO', 50), target('VT', 50)], 10_000, { wholeShares: true })
    expect(plan.trades.map((t) => [t.side, t.symbol, t.shares])).toEqual([
      ['sell', 'VOO', 40],
      ['buy', 'VT', 50],
    ])
    expect(plan.cash).toBe(0)
  })
})
