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
import type { Position, TargetWeight } from './types'

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

/** What to buy with one contribution. */
export interface Purchase {
  instrumentId: string
  symbol: string
  /** Money to put in, in minor units. */
  amount: number
  /**
   * Whole shares in whole-share mode; in money mode the estimate at the last
   * price (fractional), or null when there is no price to estimate from.
   */
  shares: number | null
  /** The instrument's share of the book after every purchase in the plan. */
  weightAfter: number
  target: number
}

export interface ContributionPlan {
  purchases: Purchase[]
  /** Money the plan could not place — whole shares that did not fit. */
  leftover: number
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
 * rounds to the minor unit with the remainder handed out by largest fraction,
 * so the amounts always add up to `amount`.
 *
 * **Whole-share mode** buys one share at a time of whichever affordable target
 * has the largest shortfall, which is the same rule made discrete, and stops
 * once nothing still short of its target fits in what is left — `leftover`.
 */
export function planContribution(
  positions: Position[],
  targets: TargetWeight[],
  amount: number,
  { wholeShares = false }: { wholeShares?: boolean } = {},
): ContributionPlan {
  const { rows, total } = drift(positions, targets)
  const priceOf = new Map<string, number | null>()
  for (const t of targets) priceOf.set(t.instrument_id, t.last_price)
  for (const p of positions) if (p.last_price !== null) priceOf.set(p.instrument_id, p.last_price)

  const skipped: ContributionPlan['skipped'] = []
  const candidates = rows
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
    return { purchases: [], leftover: Math.max(0, amount), skipped }
  }

  const bought = wholeShares
    ? wholeShareFill(candidates, amount, total + amount)
    : shortfallFill(candidates, amount, total + amount)
  const spent = bought.reduce((sum, b) => sum + b.amount, 0)
  const after = total + spent

  const purchases = candidates
    .map((c, i) => ({
      instrumentId: c.instrumentId,
      symbol: c.symbol,
      amount: bought[i].amount,
      shares: wholeShares ? bought[i].shares : c.price ? bought[i].amount / c.price : null,
      weightAfter: after === 0 ? 0 : (c.value + bought[i].amount) / after,
      target: c.target,
    }))
    .filter((p) => p.amount > 0)
    .sort((a, b) => b.amount - a.amount || a.symbol.localeCompare(b.symbol))

  return { purchases, leftover: amount - spent, skipped }
}

interface Candidate {
  target: number
  value: number
  price: number | null
}

/** Money each candidate lacks against the book `after` the contribution. */
function shortfalls(candidates: Candidate[], after: number): number[] {
  return candidates.map((c) => c.target * after - c.value)
}

function shortfallFill(candidates: Candidate[], amount: number, after: number): { amount: number; shares: number }[] {
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
  return floors.map((a) => ({ amount: a, shares: 0 }))
}

/** A guard against a pathological loop (a price of one cent and a fortune to place). */
const MAX_SHARES_BOUGHT = 100_000

function wholeShareFill(candidates: Candidate[], amount: number, after: number): { amount: number; shares: number }[] {
  const gaps = shortfalls(candidates, after)
  const out = candidates.map(() => ({ amount: 0, shares: 0 }))
  let left = amount
  for (let n = 0; n < MAX_SHARES_BOUGHT; n++) {
    let best = -1
    for (let i = 0; i < candidates.length; i++) {
      const price = candidates[i].price
      if (!price || price > left || gaps[i] <= 0) continue
      if (best === -1 || gaps[i] > gaps[best]) best = i
    }
    if (best === -1) break
    const price = candidates[best].price as number
    gaps[best] -= price
    out[best].amount += price
    out[best].shares++
    left -= price
  }
  return out
}

/** One trade in a buy-and-sell rebalance. */
export interface Trade {
  instrumentId: string
  symbol: string
  side: 'buy' | 'sell'
  /** Money moved, always positive, in minor units. */
  amount: number
  /** Whole shares in whole-share mode, else the estimate at the last price. */
  shares: number | null
  /**
   * For a sale: the realized result it would stamp, by moving-average cost and
   * before fees — the same subtraction the ledger makes. Null for a buy.
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
  /**
   * Cash left after every trade: the contribution plus sales minus buys. Zero
   * in money mode; in whole-share mode what the shares could not use, and
   * never negative — the buys are settled against what is there.
   */
  cash: number
  skipped: { symbol: string; reason: string }[]
}

/**
 * Every trade that lands one currency's book exactly on its targets with
 * `amount` of new money (0 is a rebalance of what is already there). Lines
 * outside the plan are sold out — their target is zero.
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
  { wholeShares = false }: { wholeShares?: boolean } = {},
): RebalancePlan {
  const { rows, total } = drift(positions, targets)
  const held = new Map(positions.filter((p) => p.quantity > 0).map((p) => [p.instrument_id, p]))
  const priceOf = new Map<string, number | null>()
  for (const t of targets) priceOf.set(t.instrument_id, t.last_price)
  for (const p of positions) if (p.last_price !== null) priceOf.set(p.instrument_id, p.last_price)

  const skipped: RebalancePlan['skipped'] = []
  const after = total + Math.max(0, amount)
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
      realized: side === 'sell' ? realizedOnSale(held.get(r.instrumentId), money, shares) : null,
      weightAfter: 0,
      target: goal,
    })
  }

  let cash = Math.max(0, amount) + sumOf(trades, 'sell') - sumOf(trades, 'buy')
  if (wholeShares) cash = settleWholeShares(trades, rows, priceOf, after, cash)
  // Money mode rounds each trade to the cent on its own; the odd cents left
  // over go to the largest buy, so the plan spends exactly what it has.
  if (!wholeShares && cash !== 0) {
    const largest = trades.filter((t) => t.side === 'buy').sort((a, b) => b.amount - a.amount)[0]
    if (largest && largest.amount + cash > 0) {
      largest.amount += cash
      cash = 0
    }
  }

  const finalTotal = total + Math.max(0, amount) - cash
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
    cash,
    skipped,
  }
}

/**
 * Brings whole-share buys within `cash` (trimming the buy nearest its target
 * first), then spends what is left on the largest shortfalls, one share at a
 * time. Mutates `trades`; returns the cash remaining, which is never
 * negative: with every buy trimmed away, what is left is the contribution plus
 * the sales.
 */
function settleWholeShares(
  trades: Trade[],
  rows: DriftRow[],
  priceOf: Map<string, number | null>,
  after: number,
  cash: number,
): number {
  const rowOf = new Map(rows.map((r) => [r.instrumentId, r]))
  const valueAfter = (id: string) => {
    const t = trades.find((x) => x.instrumentId === id)
    const v = rowOf.get(id)?.value ?? 0
    return t ? v + (t.side === 'buy' ? t.amount : -t.amount) : v
  }
  // Money the line still lacks against its target share of the final book.
  const shortfall = (id: string) => (rowOf.get(id)?.target ?? 0) * after - valueAfter(id)

  for (let n = 0; cash < 0 && n < MAX_SHARES_BOUGHT; n++) {
    const buys = trades.filter((t) => t.side === 'buy' && (t.shares ?? 0) > 0)
    if (buys.length === 0) break
    const trim = buys.reduce((a, b) => (shortfall(b.instrumentId) < shortfall(a.instrumentId) ? b : a))
    const price = priceOf.get(trim.instrumentId) as number
    trim.shares = (trim.shares as number) - 1
    trim.amount -= price
    cash += price
  }

  for (let n = 0; n < MAX_SHARES_BOUGHT; n++) {
    let best: DriftRow | null = null
    for (const r of rows) {
      const price = priceOf.get(r.instrumentId)
      if (r.target === null || r.target === 0 || r.value === null || !price || price > cash) continue
      // Only a share that brings the line closer to its target: one costing
      // more than twice the gap would overshoot by more than it fills.
      if (shortfall(r.instrumentId) <= price / 2) continue
      if (best === null || shortfall(r.instrumentId) > shortfall(best.instrumentId)) best = r
    }
    if (best === null) break
    const price = priceOf.get(best.instrumentId) as number
    let t = trades.find((x) => x.instrumentId === best!.instrumentId)
    if (t && t.side === 'sell') break // never buy back what the plan is selling
    if (!t) {
      t = {
        instrumentId: best.instrumentId,
        symbol: best.symbol,
        side: 'buy',
        amount: 0,
        shares: 0,
        realized: null,
        weightAfter: 0,
        target: best.target as number,
      }
      trades.push(t)
    }
    t.shares = (t.shares as number) + 1
    t.amount += price
    cash -= price
  }

  // A buy trimmed to nothing is no longer a trade.
  for (let i = trades.length - 1; i >= 0; i--) if (trades[i].side === 'buy' && trades[i].amount === 0) trades.splice(i, 1)
  return cash
}

function sumOf(trades: Trade[], side: 'buy' | 'sell'): number {
  return trades.filter((t) => t.side === side).reduce((sum, t) => sum + t.amount, 0)
}

/**
 * What selling `shares` for `proceeds` would add to realized P/L, by the rule
 * in models.PositionState.Apply: the cost removed is the position's total cost
 * times the fraction sold, rounded half-up, and all of it on a full exit.
 * Fees are not known until the trade is made, so this is before them.
 */
function realizedOnSale(p: Position | undefined, proceeds: number, shares: number | null): number | null {
  if (!p || shares === null) return null
  const held = toShares(p.quantity)
  const costRemoved = shares >= held ? p.cost_basis : Math.round((p.cost_basis * shares) / held)
  return proceeds - costRemoved
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
