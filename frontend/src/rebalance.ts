/**
 * Target allocation: how far a currency's book has drifted from the weights its
 * owner set, and how to close the gap — two ways.
 *
 * **Buy only** (`planContribution`, and `toBuy` on the drift rows) never sells.
 * It answers "I have $300 to put in — what should it buy?", and "how much would
 * I have to put in to be back on target without selling anything?". Selling
 * realizes a gain or a loss — a taxable event, and in this ledger a RealizedPL
 * stamp — so rebalancing with contributions is slower but never does that,
 * which suits a book added to every month.
 *
 * **Buy and sell** (`planRebalance`, and `toTarget` on the rows) lands every
 * line exactly on its target, selling what is over. It is the broker's
 * rebalance button, and because the cost of it is the gain or loss it banks,
 * every sale it suggests carries the realized result it would stamp, measured
 * the way the ledger will measure it (moving-average cost, before fees).
 *
 * The rules are allocation.ts's, for the same reasons: weights are fractions of
 * **one currency's priced book** (no FX rate exists), and **an unpriced holding
 * is left out and counted**, never valued at zero. A holding with a target but
 * no quote cannot be placed at all — its current weight is unknown, so whether
 * it is under or over is too — and is reported instead of being guessed at.
 *
 * Holdings with no target count toward the book's total (they are part of it)
 * but never receive money: a target set is a complete plan, so anything outside
 * it is meant to shrink as a share, and the only way to do that without selling
 * is to stop buying it.
 */
import { marketValue } from './positionMath'
import { toShares } from './shares'
import type { Position, RecurringPlan, TargetWeight } from './types'

/** One line of the drift table: a target, a holding, or both. */
export interface DriftRow {
  instrumentId: string
  symbol: string
  /** The target as a fraction, or null for a holding outside the plan. */
  target: number | null
  /** Current market value in minor units; null when unpriced. */
  value: number | null
  /** Current share of the priced book; null when unpriced or the book is empty. */
  weight: number | null
  /** weight − target (a holding outside the plan has a target of 0). */
  drift: number | null
  /** Money in (positive) or out (negative) that would land exactly on target. */
  toTarget: number | null
  /**
   * Money to put in, selling nothing, for this line to reach its target once
   * every line has (see Drift.buyOnlyTotal). Null for a line outside the plan,
   * which buying can never bring to zero, and for an unpriced one.
   */
  toBuy: number | null
}

export interface Drift {
  rows: DriftRow[]
  /** The priced market value the weights are fractions of. */
  total: number
  /** Open holdings with no quote, left out of `total`. */
  unpriced: number
  /**
   * New money that would bring every target to its weight without a sale, or
   * null with nothing in the plan priced. The most overweight line sets it:
   * nothing is sold, so the book has to grow until that line's value is its
   * target share, and everything else is bought up to match.
   */
  buyOnlyTotal: number | null
  /**
   * Priced value of the holdings outside the plan. Buying only, they never
   * reach their target of zero; they stay at this and shrink as a share.
   */
  outside: number
}

const bpsToFraction = (bps: number) => bps / 10_000

/**
 * Drift of one currency's open holdings from its targets. Both arguments must
 * already be filtered to that currency. Rows come back in target order (largest
 * first), then the holdings outside the plan, largest first.
 */
export function drift(positions: Position[], targets: TargetWeight[]): Drift {
  const open = positions.filter((p) => p.quantity > 0)
  const valueOf = new Map<string, number | null>(open.map((p) => [p.instrument_id, marketValue(p)]))
  const total = open.reduce((sum, p) => sum + (marketValue(p) ?? 0), 0)
  const unpriced = open.filter((p) => marketValue(p) === null).length

  const row = (instrumentId: string, symbol: string, target: number | null): DriftRow => {
    // Not held at all is a known zero, unlike held-but-unpriced.
    const value = valueOf.has(instrumentId) ? (valueOf.get(instrumentId) ?? null) : 0
    const weight = value === null || total === 0 ? null : value / total
    const goal = target ?? 0
    return {
      instrumentId,
      symbol,
      target,
      value,
      weight,
      drift: weight === null ? null : weight - goal,
      toTarget: value === null ? null : Math.round(goal * total - value),
      toBuy: null,
    }
  }

  const targeted = new Set(targets.map((t) => t.instrument_id))
  const rows = [
    ...[...targets]
      .sort((a, b) => b.weight_bps - a.weight_bps || a.symbol.localeCompare(b.symbol))
      .map((t) => row(t.instrument_id, t.symbol, bpsToFraction(t.weight_bps))),
    ...open
      .filter((p) => !targeted.has(p.instrument_id))
      .sort((a, b) => (marketValue(b) ?? -1) - (marketValue(a) ?? -1))
      .map((p) => row(p.instrument_id, p.symbol, null)),
  ]

  // Buy only: the plan's part of the book has to grow to the size at which its
  // most overweight line is exactly on target. Holdings outside the plan stay
  // where they are, so the targets are reached as shares of the planned part,
  // and the outside lines' share is what is left to shrink.
  const planned = rows.filter((r) => r.target !== null && r.target > 0 && r.value !== null)
  let buyOnlyTotal: number | null = null
  if (planned.length > 0) {
    const size = Math.max(...planned.map((r) => (r.value as number) / (r.target as number)))
    buyOnlyTotal = 0
    for (const r of planned) {
      r.toBuy = Math.max(0, Math.round((r.target as number) * size - (r.value as number)))
      buyOnlyTotal += r.toBuy
    }
  }
  const outside = rows
    .filter((r) => r.target === null)
    .reduce((sum, r) => sum + (r.value ?? 0), 0)
  return { rows, total, unpriced, buyOnlyTotal, outside }
}

/**
 * The fee a trade would cost, in minor units, or null when no estimate applies
 * (no fee profile fits the instrument). `shares` may be fractional. The caller
 * builds it from the fee profiles (feeMath.estimateFee), which keeps this
 * module free of settings and its tests free of fee tables.
 */
export type FeeFn = (instrumentId: string, side: 'buy' | 'sell', shares: number, price: number) => number | null

interface PlanOptions {
  wholeShares?: boolean
  /**
   * Fees to plan around. Without it the plans are gross — no fee is charged,
   * none is reported, and none is counted unknown.
   */
  fee?: FeeFn
}

/** What to buy with one contribution. */
export interface Purchase {
  instrumentId: string
  symbol: string
  /** Money that goes into the holding, in minor units — the gross, before its fee. */
  amount: number
  /**
   * Whole shares in whole-share mode; in money mode the estimate at the last
   * price (fractional), or null when there is no price to estimate from.
   */
  shares: number | null
  /** The estimated fee on top of `amount`; null when it cannot be estimated. */
  fee: number | null
  /** The instrument's share of the book after every purchase in the plan. */
  weightAfter: number
  target: number
}

export interface ContributionPlan {
  purchases: Purchase[]
  /** Money the plan could not place — whole shares that did not fit. */
  leftover: number
  /** Estimated fees across the purchases, counting only those that could be estimated. */
  fees: number
  /** Purchases whose fee could not be estimated, so `fees` is short by theirs. */
  feesUnknown: number
  /** Targets the plan could not consider, with the reason. */
  skipped: { symbol: string; reason: string }[]
}

/**
 * Splits `amount` (minor units) across one currency's targets so the book ends
 * as close to them as new money alone can bring it.
 *
 * Each target's **shortfall** is what it lacks in money once the contribution
 * is in: weight × (book + amount) − current value — the "To target" column,
 * measured against the book the contribution will make. Money goes to the
 * largest shortfall first, until it is level with the next largest, then to
 * both, and so on. That is the split leaving the smallest worst gap in
 * percentage points, which is how the drift table measures and so what a
 * reader expects: the line furthest below target is the one funded.
 *
 * Measuring the gap as a *ratio* of the target instead (value ÷ weight) was
 * tried first and rejected on real data. It funds whatever is emptiest
 * relative to its own size — a new 10% target held at zero outranks a 35%
 * target that is $1,100 short — so $300 went to two small lines while the one
 * the table showed furthest behind got nothing.
 *
 * No target is ever pushed past its weight: the shortfalls add up to at least
 * the contribution (holdings outside the plan only add to them), so the level
 * they are brought down to is never below zero. When every target is short by
 * exactly its share, the money divides in the target proportions. **Money
 * mode** (the default, for brokers that buy fractions — a savings plan does)
 * rounds to the minor unit with the remainder handed out by largest fraction.
 *
 * **Fees come out of the same money.** `amount` is what leaves the reader's
 * pocket, so with `fee` given the purchases and their fees together must fit
 * in it: money mode re-runs the split on what is left after the fees until the
 * two agree, and whole-share mode prices each share at its price plus the fee
 * it adds. A fee that cannot be estimated is counted rather than taken as 0.
 *
 * **Whole-share mode** buys one share at a time of whichever affordable target
 * has the largest shortfall, which is the same rule made discrete, and stops
 * once nothing still short of its target fits in what is left — `leftover`.
 */
export function planContribution(
  positions: Position[],
  targets: TargetWeight[],
  amount: number,
  { wholeShares = false, fee }: PlanOptions = {},
): ContributionPlan {
  const { rows, total } = drift(positions, targets)
  const priceOf = pricesOf(positions, targets)
  const feeOf = feeFor(fee)

  const skipped: ContributionPlan['skipped'] = []
  const candidates: Candidate[] = rows
    .filter((r) => r.target !== null)
    .filter((r) => {
      if (r.value === null) {
        skipped.push({ symbol: r.symbol, reason: 'no quote, so its current weight is unknown' })
        return false
      }
      if (wholeShares && !priceOf.get(r.instrumentId)) {
        skipped.push({ symbol: r.symbol, reason: 'no quote to size a whole share from' })
        return false
      }
      return true
    })
    .map((r) => ({
      instrumentId: r.instrumentId,
      symbol: r.symbol,
      target: r.target as number,
      value: r.value as number,
      price: priceOf.get(r.instrumentId) ?? null,
    }))

  if (amount <= 0 || candidates.length === 0) {
    return { purchases: [], leftover: Math.max(0, amount), fees: 0, feesUnknown: 0, skipped }
  }

  let bought: Bought[]
  if (wholeShares) {
    bought = wholeShareFill(candidates, amount, total + amount, feeOf)
  } else {
    // The fees depend on the split and the split on what the fees leave, so
    // settle it by shrinking the budget by the overrun until nothing overruns.
    // Proportional fees converge in a step or two; a flat fee in one.
    let budget = amount
    bought = []
    for (let i = 0; i < 8 && budget > 0; i++) {
      bought = moneyFill(candidates, budget, total + budget, feeOf)
      const overrun = bought.reduce((sum, b) => sum + b.amount + (b.fee ?? 0), 0) - amount
      if (overrun <= 0) break
      budget -= overrun
    }
  }
  const spent = bought.reduce((sum, b) => sum + b.amount, 0)
  const paid = spent + bought.reduce((sum, b) => sum + (b.fee ?? 0), 0)
  const after = total + spent

  const purchases = candidates
    .map((c, i) => ({
      instrumentId: c.instrumentId,
      symbol: c.symbol,
      amount: bought[i].amount,
      shares: wholeShares ? bought[i].shares : c.price ? bought[i].amount / c.price : null,
      fee: bought[i].fee,
      weightAfter: after === 0 ? 0 : (c.value + bought[i].amount) / after,
      target: c.target,
    }))
    .filter((p) => p.amount > 0)
    .sort((a, b) => b.amount - a.amount || a.symbol.localeCompare(b.symbol))

  return {
    purchases,
    leftover: amount - paid,
    fees: purchases.reduce((sum, p) => sum + (p.fee ?? 0), 0),
    feesUnknown: fee ? purchases.filter((p) => p.fee === null).length : 0,
    skipped,
  }
}

interface Candidate {
  instrumentId: string
  symbol: string
  target: number
  value: number
  price: number | null
}

interface Bought {
  amount: number
  shares: number
  fee: number | null
}

/** Quotes by instrument: a holding's own, else the target's (for one not yet held). */
function pricesOf(positions: Position[], targets: TargetWeight[]): Map<string, number | null> {
  const priceOf = new Map<string, number | null>()
  for (const t of targets) priceOf.set(t.instrument_id, t.last_price)
  for (const p of positions) if (p.last_price !== null) priceOf.set(p.instrument_id, p.last_price)
  return priceOf
}

/**
 * The fee for `shares` of an instrument, 0 for no shares at all, and always
 * null when no fee function was given — callers then treat fees as absent.
 */
function feeFor(fee: FeeFn | undefined) {
  return (instrumentId: string, side: 'buy' | 'sell', shares: number, price: number | null): number | null => {
    if (!fee || price === null) return null
    if (shares <= 0) return 0
    return fee(instrumentId, side, shares, price)
  }
}

/** Money each candidate lacks against the book `after` the contribution. */
function shortfalls(candidates: Candidate[], after: number): number[] {
  return candidates.map((c) => c.target * after - c.value)
}

function moneyFill(
  candidates: Candidate[],
  amount: number,
  after: number,
  feeOf: ReturnType<typeof feeFor>,
): Bought[] {
  const gaps = shortfalls(candidates, after)
  // Largest shortfall first. Bringing the top k down to a common level D costs
  // the sum of (gap − D) over them; find the k at which `amount` runs out.
  const order = gaps.map((_, i) => i).sort((a, b) => gaps[b] - gaps[a])
  let sum = 0
  let D = 0
  for (let k = 0; k < order.length; k++) {
    sum += gaps[order[k]]
    D = (sum - amount) / (k + 1)
    const next = order[k + 1]
    if (next === undefined || D >= gaps[next]) break
  }

  const exact = gaps.map((g) => Math.max(0, g - D))
  const floors = exact.map(Math.floor)
  let remainder = amount - floors.reduce((a, b) => a + b, 0)
  const byFraction = exact.map((_, i) => i).sort((a, b) => exact[b] - floors[b] - (exact[a] - floors[a]))
  for (const i of byFraction) {
    if (remainder <= 0) break
    if (exact[i] > 0) {
      floors[i]++
      remainder--
    }
  }
  return floors.map((a, i) => {
    const price = candidates[i].price
    const shares = price ? a / price : 0
    return { amount: a, shares: 0, fee: a > 0 ? feeOf(candidates[i].instrumentId, 'buy', shares, price) : null }
  })
}

/** A guard against a pathological loop (a price of one cent and a fortune to place). */
const MAX_SHARES_BOUGHT = 100_000

function wholeShareFill(
  candidates: Candidate[],
  amount: number,
  after: number,
  feeOf: ReturnType<typeof feeFor>,
): Bought[] {
  const gaps = shortfalls(candidates, after)
  const out: Bought[] = candidates.map(() => ({ amount: 0, shares: 0, fee: null }))
  let left = amount
  for (let n = 0; n < MAX_SHARES_BOUGHT; n++) {
    let best = -1
    let bestCost = 0
    for (let i = 0; i < candidates.length; i++) {
      const c = candidates[i]
      if (!c.price || gaps[i] <= 0) continue
      // The share's price plus whatever it adds to the line's fee: the first
      // share of a line carries the whole of a flat fee.
      const cost = c.price + ((feeOf(c.instrumentId, 'buy', out[i].shares + 1, c.price) ?? 0) - (out[i].fee ?? 0))
      if (cost > left) continue
      if (best === -1 || gaps[i] > gaps[best]) {
        best = i
        bestCost = cost
      }
    }
    if (best === -1) break
    const c = candidates[best]
    gaps[best] -= c.price as number
    out[best].shares++
    out[best].amount += c.price as number
    out[best].fee = feeOf(c.instrumentId, 'buy', out[best].shares, c.price)
    left -= bestCost
  }
  return out
}

/** One trade in a buy-and-sell rebalance. */
export interface Trade {
  instrumentId: string
  symbol: string
  side: 'buy' | 'sell'
  /** Money moved at the quote, always positive — the gross, before the fee. */
  amount: number
  /** Whole shares in whole-share mode, else the estimate at the last price. */
  shares: number | null
  /** The estimated fee, which a buy pays on top and a sale out of its proceeds. */
  fee: number | null
  /**
   * For a sale: the realized result it would stamp, by moving-average cost and
   * net of its fee when one is estimated — the same subtraction the ledger
   * makes. Null for a buy.
   */
  realized: number | null
  weightAfter: number
  /** The target; 0 for a holding outside the plan, which is sold out. */
  target: number
}

export interface RebalancePlan {
  trades: Trade[]
  /** Sum of the sales' realized results; what the rebalance would bank. */
  realized: number
  /** Estimated fees across the trades, counting only those that could be estimated. */
  fees: number
  /** Trades whose fee could not be estimated. */
  feesUnknown: number
  /**
   * Cash left after every trade and fee: the contribution plus net sales minus
   * buys with their fees. About zero in money mode; in whole-share mode what
   * the shares could not use. Never negative — the buys are settled against
   * what is there.
   */
  cash: number
  skipped: { symbol: string; reason: string }[]
}

/**
 * Every trade that lands one currency's book exactly on its targets with
 * `amount` of new money (0 is a rebalance of what is already there). Lines
 * outside the plan are sold out — their target is zero.
 *
 * Fees, when given, are paid from the same pot: a sale brings in its proceeds
 * less its fee, and a buy costs its amount plus its fee. In money mode the buys
 * are scaled down together until they and their fees fit what the sales and
 * the contribution bring in, so the book lands a fee's width short of target
 * rather than the plan quietly needing money nobody offered.
 *
 * In whole-share mode each trade is first rounded toward zero shares, so no
 * line is overshot and nothing is sold that the exact plan would not sell. The
 * rounding does not cancel out — a sale of 0.4 of a share is dropped while the
 * buys it was meant to fund are not — so the buys are then settled against the
 * cash actually there: if they cost more, shares come off the buy closest to
 * its target first until the plan is affordable; if money is left, shares are
 * added to whatever is furthest below target, the contribution planner's rule,
 * but only while a share brings its line closer to target rather than past it.
 * A plan that needs money the reader did not offer is not a plan.
 */
export function planRebalance(
  positions: Position[],
  targets: TargetWeight[],
  amount: number,
  { wholeShares = false, fee }: PlanOptions = {},
): RebalancePlan {
  const { rows, total } = drift(positions, targets)
  const held = new Map(positions.filter((p) => p.quantity > 0).map((p) => [p.instrument_id, p]))
  const priceOf = pricesOf(positions, targets)
  const feeOf = feeFor(fee)
  const contribution = Math.max(0, amount)

  const skipped: RebalancePlan['skipped'] = []
  const after = total + contribution
  const trades: Trade[] = []
  for (const r of rows) {
    if (r.value === null) {
      skipped.push({ symbol: r.symbol, reason: 'no quote, so its current value is unknown' })
      continue
    }
    const goal = r.target ?? 0
    const exact = goal * after - r.value
    const price = priceOf.get(r.instrumentId) ?? null
    if (Math.abs(exact) < 1) continue
    if (wholeShares && !price) {
      skipped.push({ symbol: r.symbol, reason: 'no quote to size a whole share from' })
      continue
    }

    let money: number
    let shares: number | null
    const position = held.get(r.instrumentId)
    if (goal === 0 && position) {
      // Selling out: the shares held, exactly, rather than value ÷ price, which
      // floating point can land a hair under the holding and leave a residue.
      const all = toShares(position.quantity)
      shares = wholeShares ? Math.trunc(all) : all
      if (shares === 0) continue
      money = shares === all ? r.value : shares * (price as number)
    } else if (wholeShares) {
      shares = Math.trunc(Math.abs(exact) / (price as number))
      if (shares === 0) continue
      money = shares * (price as number)
    } else {
      money = Math.round(Math.abs(exact))
      shares = price ? money / price : null
    }
    const side = exact > 0 ? 'buy' : 'sell'
    trades.push({
      instrumentId: r.instrumentId,
      symbol: r.symbol,
      side,
      amount: money,
      shares,
      fee: shares === null ? null : feeOf(r.instrumentId, side, shares, price),
      realized: null,
      weightAfter: 0,
      target: goal,
    })
  }

  const cashOf = () =>
    contribution +
    trades.reduce((sum, t) => sum + (t.side === 'sell' ? t.amount - (t.fee ?? 0) : -(t.amount + (t.fee ?? 0))), 0)

  if (wholeShares) {
    settleWholeShares(trades, rows, priceOf, after, cashOf, feeOf)
  } else {
    fitMoneyBuys(trades, priceOf, cashOf, feeOf)
  }
  for (const t of trades) {
    if (t.side === 'sell') t.realized = realizedOnSale(held.get(t.instrumentId), t.amount, t.shares, t.fee)
  }
  const cash = cashOf()

  const finalTotal = total + trades.reduce((sum, t) => sum + (t.side === 'buy' ? t.amount : -t.amount), 0)
  const valueOf = new Map(rows.map((r) => [r.instrumentId, r.value ?? 0]))
  for (const t of trades) {
    const v = (valueOf.get(t.instrumentId) ?? 0) + (t.side === 'buy' ? t.amount : -t.amount)
    t.weightAfter = finalTotal === 0 ? 0 : v / finalTotal
  }
  trades.sort((a, b) =>
    a.side !== b.side ? (a.side === 'sell' ? -1 : 1) : b.amount - a.amount || a.symbol.localeCompare(b.symbol),
  )
  return {
    trades,
    realized: trades.reduce((sum, t) => sum + (t.realized ?? 0), 0),
    fees: trades.reduce((sum, t) => sum + (t.fee ?? 0), 0),
    feesUnknown: fee ? trades.filter((t) => t.fee === null).length : 0,
    cash,
    skipped,
  }
}

/** Re-prices a trade at a new size: its gross at the quote and the fee on it. */
function resize(t: Trade, shares: number, price: number, feeOf: ReturnType<typeof feeFor>) {
  t.shares = shares
  t.amount = Math.round(shares * price)
  t.fee = feeOf(t.instrumentId, t.side, shares, price)
}

/**
 * Money mode: scales every buy by one factor so the buys and their fees spend
 * exactly the cash the sales and contribution bring in, then hands the odd
 * cents to the largest buy. The factor is found by iterating, since a fee
 * depends on the size it is charged on; it settles in a few rounds.
 */
function fitMoneyBuys(
  trades: Trade[],
  priceOf: Map<string, number | null>,
  cashOf: () => number,
  feeOf: ReturnType<typeof feeFor>,
) {
  const buys = trades.filter((t) => t.side === 'buy')
  if (buys.length === 0) return
  const wanted = new Map(buys.map((t) => [t, t.amount]))
  let scale = 1
  for (let i = 0; i < 8; i++) {
    const cash = cashOf()
    if (Math.abs(cash) < 1) break
    const gross = buys.reduce((sum, t) => sum + t.amount, 0)
    if (gross === 0) break
    scale = Math.max(0, scale * ((gross + cash) / gross))
    for (const t of buys) {
      const price = priceOf.get(t.instrumentId) ?? null
      const money = Math.round((wanted.get(t) as number) * scale)
      t.amount = money
      t.shares = price ? money / price : null
      t.fee = t.shares === null ? null : feeOf(t.instrumentId, 'buy', t.shares, price)
    }
  }
  // Rounding leaves a cent or two either way; the largest buy absorbs it.
  const cash = Math.round(cashOf())
  const largest = [...buys].sort((a, b) => b.amount - a.amount)[0]
  if (cash !== 0 && largest.amount + cash > 0) {
    largest.amount += cash
    const price = priceOf.get(largest.instrumentId) ?? null
    largest.shares = price ? largest.amount / price : null
  }
  for (let i = trades.length - 1; i >= 0; i--) if (trades[i].side === 'buy' && trades[i].amount <= 0) trades.splice(i, 1)
}

/**
 * Brings whole-share buys within the cash there is (trimming the buy nearest
 * its target first), then spends what is left on the largest shortfalls, one
 * share at a time, each costing its price plus the fee it adds. Mutates
 * `trades`. The cash never ends negative: with every buy trimmed away, what is
 * left is the contribution plus the sales net of their fees.
 */
function settleWholeShares(
  trades: Trade[],
  rows: DriftRow[],
  priceOf: Map<string, number | null>,
  after: number,
  cashOf: () => number,
  feeOf: ReturnType<typeof feeFor>,
) {
  const rowOf = new Map(rows.map((r) => [r.instrumentId, r]))
  const valueAfter = (id: string) => {
    const t = trades.find((x) => x.instrumentId === id)
    const v = rowOf.get(id)?.value ?? 0
    return t ? v + (t.side === 'buy' ? t.amount : -t.amount) : v
  }
  // Money the line still lacks against its target share of the final book.
  const shortfall = (id: string) => (rowOf.get(id)?.target ?? 0) * after - valueAfter(id)

  for (let n = 0; cashOf() < 0 && n < MAX_SHARES_BOUGHT; n++) {
    const buys = trades.filter((t) => t.side === 'buy' && (t.shares ?? 0) > 0)
    if (buys.length === 0) break
    const trim = buys.reduce((a, b) => (shortfall(b.instrumentId) < shortfall(a.instrumentId) ? b : a))
    resize(trim, (trim.shares as number) - 1, priceOf.get(trim.instrumentId) as number, feeOf)
  }

  for (let n = 0; n < MAX_SHARES_BOUGHT; n++) {
    let best: DriftRow | null = null
    for (const r of rows) {
      const price = priceOf.get(r.instrumentId)
      if (r.target === null || r.target === 0 || r.value === null || !price) continue
      // Only a share that brings the line closer to its target: one costing
      // more than twice the gap would overshoot by more than it fills.
      if (shortfall(r.instrumentId) <= price / 2) continue
      const existing = trades.find((x) => x.instrumentId === r.instrumentId)
      if (existing && existing.side === 'sell') continue // never buy back what the plan is selling
      const shares = (existing?.shares ?? 0) + 1
      const extra = price + ((feeOf(r.instrumentId, 'buy', shares, price) ?? 0) - (existing?.fee ?? 0))
      if (extra > cashOf()) continue
      if (best === null || shortfall(r.instrumentId) > shortfall(best.instrumentId)) best = r
    }
    if (best === null) break
    const price = priceOf.get(best.instrumentId) as number
    let t = trades.find((x) => x.instrumentId === best!.instrumentId)
    if (!t) {
      t = {
        instrumentId: best.instrumentId,
        symbol: best.symbol,
        side: 'buy',
        amount: 0,
        shares: 0,
        fee: 0,
        realized: null,
        weightAfter: 0,
        target: best.target as number,
      }
      trades.push(t)
    }
    resize(t, (t.shares as number) + 1, price, feeOf)
  }

  // A buy trimmed to nothing is no longer a trade.
  for (let i = trades.length - 1; i >= 0; i--) if (trades[i].side === 'buy' && trades[i].amount === 0) trades.splice(i, 1)
}

/**
 * What selling `shares` for `proceeds` would add to realized P/L, by the rule
 * in models.PositionState.Apply: (proceeds − fee) − cost removed, where the
 * cost removed is the position's total cost times the fraction sold, rounded
 * half-up, and all of it on a full exit. An unestimated fee is left out, which
 * the plan reports by counting it.
 */
function realizedOnSale(
  p: Position | undefined,
  proceeds: number,
  shares: number | null,
  fee: number | null,
): number | null {
  if (!p || shares === null) return null
  const held = toShares(p.quantity)
  const costRemoved = shares >= held ? p.cost_basis : Math.round((p.cost_basis * shares) / held)
  return proceeds - (fee ?? 0) - costRemoved
}

/** One line of the savings-plan suggestion. */
export interface SavingsLine {
  instrumentId: string
  symbol: string
  /** The target, or null for an instrument a plan buys that is outside it. */
  target: number | null
  /** What the active plans put into it a month now, in minor units. */
  current: number
  /** What they should put in, so the book reaches its targets over the horizon. */
  suggested: number
}

/** A plan's monthly outlay: its amount on each of its days. */
export function monthlyOutlay(plan: RecurringPlan): number {
  return plan.amount * plan.days_of_month.split(',').filter((d) => d.trim() !== '').length
}

/** Whether a plan still buys on or after `today` (YYYY-MM-DD). */
export function isActivePlan(plan: RecurringPlan, today: string): boolean {
  return plan.ended_on === '' || plan.ended_on >= today
}

/**
 * How a monthly savings budget should be split across one currency's targets,
 * so that the book reaches them over `months` months of saving.
 *
 * It is the contribution planner asked about the whole horizon at once — the
 * budget × months, split largest shortfall first — and then divided back into
 * a month. That one rule covers both phases a savings plan goes through: while
 * the book is off target the money leans into what is short, and once the
 * shortfalls are levelled whatever remains divides in the target proportions,
 * which is the steady state. A shorter horizon leans harder; a long one is
 * close to the plain target split. Instruments a plan buys today but the
 * targets leave out are listed with a suggestion of zero — the plan that would
 * stop.
 *
 * Amounts are gross, as a plan's amount is (`RecurringPlan.Amount` is what is
 * debited for the shares; the fee is charged on top), and rounded to whole
 * currency units when the budget is one — a broker takes a plan in whole
 * amounts — with the remainder handed out by largest fraction so the lines add
 * up to the budget exactly. Prices play no part: a plan buys money's worth, so
 * neither whole shares nor a missing quote on a target not yet held matters,
 * although a held line with no quote still cannot be placed.
 */
export function planSavings(
  positions: Position[],
  targets: TargetWeight[],
  plans: RecurringPlan[],
  monthly: number,
  months: number,
  today: string,
): { lines: SavingsLine[]; skipped: ContributionPlan['skipped'] } {
  const current = new Map<string, { symbol: string; amount: number }>()
  for (const p of plans) {
    if (!isActivePlan(p, today)) continue
    const line = current.get(p.instrument_id) ?? { symbol: p.symbol, amount: 0 }
    line.amount += monthlyOutlay(p)
    current.set(p.instrument_id, line)
  }

  const horizon = Math.max(1, Math.round(months))
  const plan = planContribution(positions, targets, Math.max(0, monthly) * horizon)
  const perMonth = new Map(plan.purchases.map((p) => [p.instrumentId, p.amount / horizon]))

  // Whole currency units when the budget is one, else cents.
  const unit = monthly % 100 === 0 ? 100 : 1
  const ids = targets.map((t) => t.instrument_id)
  const exact = ids.map((id) => (perMonth.get(id) ?? 0) / unit)
  const floors = exact.map(Math.floor)
  let remainder = Math.round(monthly / unit) - floors.reduce((a, b) => a + b, 0)
  const byFraction = exact.map((_, i) => i).sort((a, b) => exact[b] - floors[b] - (exact[a] - floors[a]))
  for (const i of byFraction) {
    if (remainder <= 0) break
    if (exact[i] > 0) {
      floors[i]++
      remainder--
    }
  }

  const lines: SavingsLine[] = targets.map((t, i) => ({
    instrumentId: t.instrument_id,
    symbol: t.symbol,
    target: t.weight_bps / 10_000,
    current: current.get(t.instrument_id)?.amount ?? 0,
    suggested: floors[i] * unit,
  }))
  for (const [id, line] of current) {
    if (!ids.includes(id)) {
      lines.push({ instrumentId: id, symbol: line.symbol, target: null, current: line.amount, suggested: 0 })
    }
  }
  lines.sort((a, b) => b.suggested - a.suggested || b.current - a.current || a.symbol.localeCompare(b.symbol))
  return { lines, skipped: plan.skipped }
}

/**
 * A starting set of targets from the book as it stands: each priced holding at
 * its current weight, rounded to whole percent, with the rounding difference
 * given to the largest so the set adds up to exactly 100%. Editing from here is
 * quicker than typing every weight from zero, and starts with no drift at all.
 */
export function targetsFromCurrent(positions: Position[]): { instrumentId: string; weightBps: number }[] {
  const priced = positions
    .filter((p) => p.quantity > 0 && marketValue(p) !== null)
    .map((p) => ({ instrumentId: p.instrument_id, value: marketValue(p) as number }))
    .filter((p) => p.value > 0)
    .sort((a, b) => b.value - a.value)
  const total = priced.reduce((sum, p) => sum + p.value, 0)
  if (total === 0) return []
  const out = priced.map((p) => ({ instrumentId: p.instrumentId, weightBps: Math.round((p.value / total) * 100) * 100 }))
  const sum = out.reduce((s, t) => s + t.weightBps, 0)
  out[0].weightBps += 10_000 - sum
  return out.filter((t) => t.weightBps > 0)
}
