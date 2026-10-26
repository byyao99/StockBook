<script setup lang="ts">
/**
 * Target allocation and the planner, one block per currency.
 *
 * The allocation bar above this on /positions says what the book *is*; this
 * says what it was *meant* to be, how far it has drifted, and how to close the
 * gap — in one of two modes the reader switches between. **Buy only** never
 * sells: the table says how much each line needs bought to be back on target,
 * and the planner splits a contribution. **Buy and sell** lands every line on
 * target: the table is signed, and the planner lists the trades with the gain
 * or loss each sale would realize, because that is what selling costs. The
 * arithmetic lives in rebalance.ts, unit-tested; this component only edits the
 * set and renders what that module returns.
 *
 * Fees are estimated from the reader's own fee profiles (Account page), under
 * the profile the trade form would default to for each instrument, and paid
 * out of the same money: a suggestion never needs more than was offered. A
 * savings-plan panel splits a monthly budget across the targets, beside what
 * the active plans put in now.
 *
 * Targets are edited as one set across every currency and saved whole,
 * because the server only accepts a set in which each currency adds up to
 * 100%: weights edited one at a time would pass through states that do not.
 *
 * A plan is a suggestion and nothing more. It never fills the trade form in —
 * an order not yet placed has no fill price, and the trade worth recording is
 * the one the broker confirms.
 */
import { computed, onMounted, ref, watch } from 'vue'
import { instrumentApi, planApi, settingsApi } from '../api/client'
import { defaultProfileKey, estimateFee, findProfile } from '../feeMath'
import {
  formatCents,
  formatCentsOrUnknown,
  formatPercentMagnitudeOrUnknown,
  formatSignedCents,
  formatSignedOrUnknown,
  fromCents,
  toCents,
} from '../money'
import { drift, isActivePlan, monthlyOutlay, planContribution, planRebalance, planSavings, targetsFromCurrent } from '../rebalance'
import type { FeeFn } from '../rebalance'
import { ESTIMATE_DECIMALS, formatShares, fromShares } from '../shares'
import type { Currency, FeeProfile, Instrument, Position, RecurringPlan, TargetWeight } from '../types'
import InstrumentPicker from './InstrumentPicker.vue'

const props = defineProps<{
  /** Every open holding, across currencies — the page already loads them all. */
  positions: Position[]
}>()

const targets = ref<TargetWeight[]>([])
const instruments = ref<Instrument[]>([])
const feeProfiles = ref<FeeProfile[]>([])
const savingsPlans = ref<RecurringPlan[]>([])
const error = ref('')
const success = ref('')
const saving = ref(false)

/** A drift this far from target, either way, is called out. */
const FAR_DRIFT = 0.05

// Which way to close the gap. Buy only is the default: it is the one that
// cannot realize anything by accident. The choice is a per-viewer convenience,
// so it is remembered in this browser and nowhere else.
type Mode = 'buy' | 'rebalance'
const MODE_KEY = 'stockbook.targets.mode'
function readMode(): Mode {
  try {
    return localStorage.getItem(MODE_KEY) === 'rebalance' ? 'rebalance' : 'buy'
  } catch {
    return 'buy'
  }
}
const mode = ref<Mode>(readMode())
watch(mode, (m) => {
  try {
    localStorage.setItem(MODE_KEY, m)
  } catch {
    // Private windows and blocked storage: the choice simply is not kept.
  }
})

// Currencies this block speaks to: any the book holds or the plan names.
const currencies = computed<Currency[]>(() => {
  const set = new Set<Currency>()
  for (const p of props.positions) set.add(p.currency)
  for (const t of targets.value) set.add(t.currency)
  return [...set].sort()
})

const heldIn = (c: Currency) => props.positions.filter((p) => p.currency === c)
const targetsIn = (c: Currency) => targets.value.filter((t) => t.currency === c)

const drifts = computed(() => new Map(currencies.value.map((c) => [c, drift(heldIn(c), targetsIn(c))])))

// The planner's inputs, per currency.
const amounts = ref<Record<string, number | null>>({})
const wholeShares = ref<Record<string, boolean>>({})

/**
 * The fee a suggested trade would cost, under the profile the trade form would
 * pick for that instrument as an ordinary (not scheduled) trade. Null when no
 * profile applies — a market with no fee terms — which the plan counts.
 */
const fee = computed<FeeFn | undefined>(() => {
  if (feeProfiles.value.length === 0) return undefined
  const kind = new Map<string, { market: string; asset_type: string }>()
  for (const t of targets.value) kind.set(t.instrument_id, t)
  for (const p of props.positions) kind.set(p.instrument_id, p)
  return (id, side, shares, price) => {
    const key = defaultProfileKey(kind.get(id) as Instrument | undefined, false)
    return estimateFee(findProfile(feeProfiles.value, key ?? ''), side, shares, price)
  }
})

const plans = computed(
  () =>
    new Map(
      currencies.value.map((c) => {
        const dollars = amounts.value[c]
        if (mode.value !== 'buy' || !dollars || dollars <= 0 || targetsIn(c).length === 0) return [c, null]
        return [
          c,
          planContribution(heldIn(c), targetsIn(c), toCents(dollars), {
            wholeShares: !!wholeShares.value[c],
            fee: fee.value,
          }),
        ]
      }),
    ),
)

// A rebalance needs no new money — with none it rebalances what is there — so
// it is computed whenever the mode is on, with the amount as an optional extra.
const rebalances = computed(
  () =>
    new Map(
      currencies.value.map((c) => {
        if (mode.value !== 'rebalance' || targetsIn(c).length === 0) return [c, null]
        const extra = toCents(Math.max(0, amounts.value[c] ?? 0))
        return [c, planRebalance(heldIn(c), targetsIn(c), extra, { wholeShares: !!wholeShares.value[c], fee: fee.value })]
      }),
    ),
)

// --- Savings plans ---------------------------------------------------------

function todayUTC(): string {
  return new Date().toISOString().slice(0, 10)
}

const plansIn = (c: Currency) => savingsPlans.value.filter((p) => p.currency === c)

/** What the active plans in a currency put in a month now. */
const currentMonthly = (c: Currency) =>
  plansIn(c)
    .filter((p) => isActivePlan(p, todayUTC()))
    .reduce((sum, p) => sum + monthlyOutlay(p), 0)

// The budget starts at what the plans already spend, so the first reading is
// "the same money, split by the targets" — then the reader can change it.
const budgets = ref<Record<string, number | null>>({})
const horizons = ref<Record<string, number>>({})

const savings = computed(
  () =>
    new Map(
      currencies.value.map((c) => {
        const dollars = budgets.value[c]
        if (!dollars || dollars <= 0 || targetsIn(c).length === 0) return [c, null]
        return [
          c,
          planSavings(heldIn(c), targetsIn(c), plansIn(c), toCents(dollars), horizons.value[c] ?? 12, todayUTC()),
        ]
      }),
    ),
)

/** Symbols outside the plan in a currency, for the buy-only note. */
const outsideSymbols = (c: Currency) =>
  drifts.value
    .get(c)!
    .rows.filter((r) => r.target === null && r.value !== null)
    .map((r) => r.symbol)
    .join(', ')

function formatDrift(d: number | null): string {
  if (d === null) return '—'
  const pp = d * 100
  if (Math.abs(pp) < 0.05) return '0.0 pp'
  return `${pp > 0 ? '+' : '−'}${Math.abs(pp).toFixed(1)} pp`
}

function driftClass(d: number | null): string {
  if (d === null || Math.abs(d) < FAR_DRIFT) return 'muted'
  return d > 0 ? 'over' : 'under'
}

/** "Fees ≈ $9.00 at your fee profiles", noting any that could not be estimated. */
function feeLine(total: number, unknown: number, c: Currency): string {
  let line = `Fees ≈ ${formatCents(total, c)}, estimated from your fee profiles and paid out of the same money.`
  if (unknown > 0) line += ` ${unknown} could not be estimated (no profile fits) and are not included.`
  return line
}

function sharesLabel(shares: number | null, whole: boolean): string {
  if (shares === null) return '—'
  if (whole) return Number.isInteger(shares) ? `${shares}` : formatShares(fromShares(shares))
  return `≈ ${formatShares(fromShares(shares), ESTIMATE_DECIMALS)}`
}

// --- Editing ---------------------------------------------------------------

interface DraftRow {
  instrumentId: string
  symbol: string
  currency: Currency
  percent: number | null
}

const editing = ref(false)
const draft = ref<DraftRow[]>([])
const adding = ref('')

const draftIn = (c: Currency) => draft.value.filter((r) => r.currency === c)
const bpsOf = (r: DraftRow) => Math.round((r.percent ?? 0) * 100)
const draftSumBps = (c: Currency) => draftIn(c).reduce((s, r) => s + bpsOf(r), 0)

// Currencies shown while editing: the plan's, plus any held, plus any a new
// row was just added in.
const draftCurrencies = computed<Currency[]>(() => {
  const set = new Set<Currency>(currencies.value)
  for (const r of draft.value) set.add(r.currency)
  return [...set].sort()
})

const draftProblems = computed(() => {
  const out: string[] = []
  for (const c of draftCurrencies.value) {
    const rows = draftIn(c)
    if (rows.length === 0) continue
    if (rows.some((r) => !r.percent || r.percent <= 0)) out.push(`every ${c} weight must be more than 0%`)
    const sum = draftSumBps(c)
    if (sum !== 10_000) out.push(`${c} adds up to ${(sum / 100).toFixed(2)}%, not 100%`)
  }
  return out
})

function startEdit() {
  error.value = ''
  success.value = ''
  draft.value = targets.value.map((t) => ({
    instrumentId: t.instrument_id,
    symbol: t.symbol,
    currency: t.currency,
    percent: t.weight_bps / 100,
  }))
  editing.value = true
}

function cancelEdit() {
  editing.value = false
  adding.value = ''
}

function removeRow(row: DraftRow) {
  draft.value = draft.value.filter((r) => r !== row)
}

// Replaces one currency's rows with the book as it stands, which starts the
// plan at zero drift and leaves only the changes to type.
function fromCurrent(c: Currency) {
  const symbolOf = new Map(heldIn(c).map((p) => [p.instrument_id, p.symbol]))
  draft.value = [
    ...draft.value.filter((r) => r.currency !== c),
    ...targetsFromCurrent(heldIn(c)).map((t) => ({
      instrumentId: t.instrumentId,
      symbol: symbolOf.get(t.instrumentId) ?? t.instrumentId,
      currency: c,
      percent: t.weightBps / 100,
    })),
  ]
}

async function loadInstruments() {
  try {
    instruments.value = (await instrumentApi.list(100, 0)).items
  } catch (e) {
    error.value = (e as Error).message
  }
}

// The picker reports an instrument it just created before the list holds it,
// so a miss reloads the list once before giving up.
watch(adding, async (id) => {
  if (!id) return
  let instrument = instruments.value.find((i) => i.id === id)
  if (!instrument) {
    await loadInstruments()
    instrument = instruments.value.find((i) => i.id === id)
  }
  if (instrument && !draft.value.some((r) => r.instrumentId === id)) {
    draft.value.push({ instrumentId: id, symbol: instrument.symbol, currency: instrument.currency, percent: null })
  }
  adding.value = ''
})

async function save() {
  error.value = ''
  success.value = ''
  saving.value = true
  try {
    targets.value = await settingsApi.saveTargets(
      draft.value.map((r) => ({ instrument_id: r.instrumentId, weight_bps: bpsOf(r) })),
    )
    editing.value = false
    success.value = targets.value.length > 0 ? 'Targets saved.' : 'Targets cleared.'
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    saving.value = false
  }
}

onMounted(async () => {
  try {
    targets.value = await settingsApi.targets()
  } catch (e) {
    error.value = (e as Error).message
  }
  // Neither is essential: without fee profiles the plans are gross and say
  // so, and without the plans list the savings panel starts from an empty
  // budget. Neither failure is worth a red banner over the whole block.
  const [profiles, plansList] = await Promise.allSettled([settingsApi.feeProfiles(), planApi.list()])
  if (profiles.status === 'fulfilled') feeProfiles.value = profiles.value
  if (plansList.status === 'fulfilled') {
    savingsPlans.value = plansList.value
    for (const c of currencies.value) {
      const now = currentMonthly(c)
      if (budgets.value[c] == null && now > 0) budgets.value[c] = fromCents(now)
    }
  }
  await loadInstruments()
})
</script>

<template>
  <section class="card">
    <div class="head">
      <h2 class="section-title">Target allocation</h2>
      <div class="head-actions">
        <div v-if="!editing && targets.length > 0" class="modes" role="group" aria-label="How to close the gap">
          <button
            v-for="m in [
              { key: 'buy', label: 'Buy only' },
              { key: 'rebalance', label: 'Buy and sell' },
            ]"
            :key="m.key"
            type="button"
            class="mode"
            :class="{ active: mode === m.key }"
            :aria-pressed="mode === m.key"
            @click="mode = m.key as Mode"
          >
            {{ m.label }}
          </button>
        </div>
        <button v-if="!editing" type="button" class="btn-secondary" @click="startEdit">
          {{ targets.length > 0 ? 'Edit targets' : 'Set targets' }}
        </button>
      </div>
    </div>
    <p class="muted intro">
      What the book is meant to look like and how far it has drifted. <strong>Buy only</strong>
      closes the gap with new money and never sells, so nothing is realized;
      <strong>Buy and sell</strong> lands every line on target and shows what each sale would
      realize. Weights are shares of each currency's own book.
    </p>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="success" class="success">{{ success }}</p>

    <!-- Viewing ---------------------------------------------------------- -->
    <template v-if="!editing">
      <p v-if="currencies.length === 0" class="muted">Nothing held or planned yet.</p>
      <section v-for="c in currencies" :key="c" class="currency-plan">
        <h3 class="currency-title">{{ c }}</h3>
        <p v-if="targetsIn(c).length === 0" class="muted">
          No {{ c }} targets yet — <strong>Set targets</strong> to start, or start from the
          current weights there.
        </p>
        <template v-else>
          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Symbol</th>
                  <th class="num">Target</th>
                  <th class="num">Now</th>
                  <th class="num">Drift</th>
                  <th class="num">{{ mode === 'buy' ? 'To buy' : 'Buy / sell' }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in drifts.get(c)!.rows" :key="r.instrumentId">
                  <td>
                    <strong>{{ r.symbol }}</strong>
                    <span v-if="r.target === null" class="muted"> · not in the plan</span>
                  </td>
                  <td class="num">{{ r.target === null ? '—' : formatPercentMagnitudeOrUnknown(r.target) }}</td>
                  <td class="num">{{ formatPercentMagnitudeOrUnknown(r.weight) }}</td>
                  <td class="num" :class="driftClass(r.drift)">{{ formatDrift(r.drift) }}</td>
                  <td v-if="mode === 'buy'" class="num">
                    {{ r.toBuy === null ? '—' : r.toBuy === 0 ? '—' : formatCents(r.toBuy, c) }}
                  </td>
                  <td v-else class="num" :class="(r.toTarget ?? 0) < 0 ? 'sell' : ''">
                    {{ formatSignedOrUnknown(r.toTarget, c) }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-if="mode === 'buy' && drifts.get(c)!.buyOnlyTotal !== null" class="muted table-note">
            <template v-if="drifts.get(c)!.buyOnlyTotal === 0">Every target is met without buying anything.</template>
            <template v-else>
              Without selling, <strong>{{ formatCents(drifts.get(c)!.buyOnlyTotal!, c) }}</strong> of new
              money would bring every target to its weight — the most overweight line sets how big the
              book has to grow.
            </template>
            <template v-if="drifts.get(c)!.outside > 0">
              {{ outsideSymbols(c) }}, outside the plan, would stay at
              {{ formatCents(drifts.get(c)!.outside, c) }}: buying never brings a holding to zero, so the
              targets are reached as shares of the rest of the book.
            </template>
          </p>
          <p v-if="drifts.get(c)!.unpriced > 0" class="notice">
            {{ drifts.get(c)!.unpriced }} {{ c }}
            {{ drifts.get(c)!.unpriced === 1 ? 'holding has' : 'holdings have' }} no quote and
            {{ drifts.get(c)!.unpriced === 1 ? 'is' : 'are' }} left out, so these weights cover only
            part of the book. Refresh quotes above.
          </p>

          <div class="planner">
            <h4 class="planner-title">{{ mode === 'buy' ? 'Next contribution' : 'Rebalance' }}</h4>
            <div class="planner-inputs">
              <label>
                {{ mode === 'buy' ? 'Amount' : 'New money, optional' }} ({{ c }})
                <input
                  v-model.number="amounts[c]"
                  type="number"
                  min="0"
                  step="1"
                  :placeholder="mode === 'buy' ? 'e.g. 300' : '0'"
                />
              </label>
              <label class="checkbox">
                <input v-model="wholeShares[c]" type="checkbox" />
                Whole shares only
              </label>
            </div>

            <template v-if="plans.get(c)">
              <p v-if="plans.get(c)!.purchases.length === 0" class="muted">
                {{
                  wholeShares[c]
                    ? 'Not enough to buy a whole share of anything in the plan.'
                    : 'Nothing in the plan can be bought.'
                }}
              </p>
              <div v-else class="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Buy</th>
                      <th class="num">Amount</th>
                      <th class="num">Shares</th>
                      <th v-if="fee" class="num">Fee</th>
                      <th class="num">Weight after</th>
                      <th class="num">Target</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="p in plans.get(c)!.purchases" :key="p.instrumentId">
                      <td><strong>{{ p.symbol }}</strong></td>
                      <td class="num">{{ formatCents(p.amount, c) }}</td>
                      <td class="num">{{ sharesLabel(p.shares, !!wholeShares[c]) }}</td>
                      <td v-if="fee" class="num">{{ formatCentsOrUnknown(p.fee, c) }}</td>
                      <td class="num">{{ formatPercentMagnitudeOrUnknown(p.weightAfter) }}</td>
                      <td class="num muted">{{ formatPercentMagnitudeOrUnknown(p.target) }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <p v-if="fee && plans.get(c)!.purchases.length > 0" class="muted planner-note">
                {{ feeLine(plans.get(c)!.fees, plans.get(c)!.feesUnknown, c) }}
              </p>
              <p v-if="plans.get(c)!.leftover > 0" class="muted planner-note">
                {{ formatCents(plans.get(c)!.leftover, c) }} left over — not enough for another whole
                share of anything below its target.
              </p>
              <p v-for="s in plans.get(c)!.skipped" :key="s.symbol" class="notice">
                {{ s.symbol }} is left out: {{ s.reason }}.
              </p>
              <p class="muted planner-note">
                Sized at the latest quotes{{ wholeShares[c] ? '' : '; share counts are estimates' }}.
                This is a suggestion — record the trades once the broker confirms what filled.
              </p>
            </template>

            <template v-if="rebalances.get(c)">
              <p v-if="rebalances.get(c)!.trades.length === 0" class="muted">
                {{
                  wholeShares[c]
                    ? 'Already as close to target as whole shares allow.'
                    : 'Already on target.'
                }}
              </p>
              <div v-else class="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Trade</th>
                      <th class="num">Amount</th>
                      <th class="num">Shares</th>
                      <th v-if="fee" class="num">Fee</th>
                      <th class="num">Realized</th>
                      <th class="num">Weight after</th>
                      <th class="num">Target</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="t in rebalances.get(c)!.trades" :key="t.instrumentId">
                      <td>
                        <span :class="['badge', t.side === 'buy' ? 'badge-buy' : 'badge-sell']">{{ t.side }}</span>
                        <strong class="trade-symbol">{{ t.symbol }}</strong>
                      </td>
                      <td class="num">{{ formatCents(t.amount, c) }}</td>
                      <!-- Selling a line out is the shares held, exactly — not an estimate. -->
                      <td class="num">{{ sharesLabel(t.shares, !!wholeShares[c] || (t.side === 'sell' && t.target === 0)) }}</td>
                      <td v-if="fee" class="num">{{ formatCentsOrUnknown(t.fee, c) }}</td>
                      <!-- A buy realizes nothing, which is an em dash, not $0.00. -->
                      <td class="num" :class="t.realized === null ? 'muted' : t.realized < 0 ? 'loss' : 'gain'">
                        {{ formatSignedOrUnknown(t.realized, c) }}
                      </td>
                      <td class="num">{{ formatPercentMagnitudeOrUnknown(t.weightAfter) }}</td>
                      <td class="num muted">{{ formatPercentMagnitudeOrUnknown(t.target) }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <p v-if="rebalances.get(c)!.trades.some((t) => t.side === 'sell')" class="notice">
                Selling would realize
                <strong>{{ formatSignedCents(rebalances.get(c)!.realized, c) }}</strong> at moving-average
                cost, {{ fee ? 'after the estimated fees' : 'before fees' }} and before any tax — the cost
                that Buy only avoids.
              </p>
              <p v-if="fee && rebalances.get(c)!.trades.length > 0" class="muted planner-note">
                {{ feeLine(rebalances.get(c)!.fees, rebalances.get(c)!.feesUnknown, c) }}
              </p>
              <p v-if="rebalances.get(c)!.cash > 0" class="muted planner-note">
                {{ formatCents(rebalances.get(c)!.cash, c) }} left over — not enough for another whole share
                of anything below its target, fee included.
              </p>
              <p v-for="s in rebalances.get(c)!.skipped" :key="s.symbol" class="notice">
                {{ s.symbol }} is left out: {{ s.reason }}.
              </p>
              <p class="muted planner-note">
                Sized at the latest quotes{{ wholeShares[c] ? '' : '; share counts are estimates' }}.
                This is a suggestion — record the trades once the broker confirms what filled.
              </p>
            </template>
          </div>

          <div class="planner">
            <h4 class="planner-title">Savings plans</h4>
            <p class="muted planner-lead">
              How a monthly budget should be split across the targets. Off target it leans into what is
              short — harder over a shorter horizon — and once on target it divides by the weights.
            </p>
            <div class="planner-inputs">
              <label>
                Per month ({{ c }})
                <input v-model.number="budgets[c]" type="number" min="0" step="1" placeholder="e.g. 500" />
              </label>
              <label>
                Reach targets over
                <select
                  :value="horizons[c] ?? 12"
                  @change="horizons[c] = Number(($event.target as HTMLSelectElement).value)"
                >
                  <option v-for="m in [3, 6, 12, 24, 36, 60]" :key="m" :value="m">{{ m }} months</option>
                </select>
              </label>
            </div>
            <p v-if="currentMonthly(c) > 0" class="muted planner-note">
              Your active {{ c }} plans put in {{ formatCents(currentMonthly(c), c) }} a month now.
            </p>
            <div v-if="savings.get(c)" class="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Plan</th>
                    <th class="num">Target</th>
                    <th class="num">Now / month</th>
                    <th class="num">Suggested / month</th>
                    <th class="num">Change</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="l in savings.get(c)!.lines" :key="l.instrumentId">
                    <td>
                      <strong>{{ l.symbol }}</strong>
                      <span v-if="l.target === null" class="muted"> · not in the targets</span>
                    </td>
                    <td class="num muted">{{ l.target === null ? '—' : formatPercentMagnitudeOrUnknown(l.target) }}</td>
                    <td class="num">{{ l.current > 0 ? formatCents(l.current, c) : '—' }}</td>
                    <td class="num">{{ l.suggested > 0 ? formatCents(l.suggested, c) : '—' }}</td>
                    <td class="num" :class="l.suggested - l.current < 0 ? 'sell' : ''">
                      {{
                        l.suggested === l.current
                          ? 'no change'
                          : l.current === 0
                            ? 'start'
                            : l.suggested === 0
                              ? 'stop'
                              : formatSignedCents(l.suggested - l.current, c)
                      }}
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-for="s in savings.get(c)?.skipped ?? []" :key="s.symbol" class="notice">
              {{ s.symbol }} is left out: {{ s.reason }}.
            </p>
            <p v-if="savings.get(c)" class="muted planner-note">
              Amounts are what each plan debits for shares, before its fee. To change one, change it at the
              broker, then on the Ledger page stop the old plan and add the new one from the day it
              changes — editing a plan rewrites it backwards, which would re-price instalments already due.
            </p>
          </div>
        </template>
      </section>
    </template>

    <!-- Editing ---------------------------------------------------------- -->
    <template v-else>
      <section v-for="c in draftCurrencies" :key="c" class="currency-plan">
        <div class="edit-head">
          <h3 class="currency-title">{{ c }}</h3>
          <button
            v-if="heldIn(c).length > 0"
            type="button"
            class="link-button"
            @click="fromCurrent(c)"
          >
            start from current weights
          </button>
        </div>
        <p v-if="draftIn(c).length === 0" class="muted">No targets — this currency is unplanned.</p>
        <ul v-else class="edit-list">
          <li v-for="r in draftIn(c)" :key="r.instrumentId">
            <strong class="edit-symbol">{{ r.symbol }}</strong>
            <input
              v-model.number="r.percent"
              type="number"
              min="0"
              max="100"
              step="0.01"
              :aria-label="`${r.symbol} target percent`"
            />
            <span class="muted">%</span>
            <button type="button" class="link-button" @click="removeRow(r)">remove</button>
          </li>
        </ul>
        <p v-if="draftIn(c).length > 0" class="sum" :class="draftSumBps(c) === 10_000 ? 'ok' : 'off'">
          Total {{ (draftSumBps(c) / 100).toFixed(2) }}%
          <span v-if="draftSumBps(c) !== 10_000">— must be 100%</span>
        </p>
      </section>

      <div class="field add-field">
        <label>Add an instrument</label>
        <InstrumentPicker
          v-model="adding"
          :instruments="instruments"
          @created="loadInstruments"
          @error="error = $event"
        />
      </div>

      <p v-for="p in draftProblems" :key="p" class="error">{{ p }}</p>
      <div class="actions">
        <button type="button" class="btn-primary" :disabled="saving || draftProblems.length > 0" @click="save">
          {{ saving ? 'Saving…' : 'Save targets' }}
        </button>
        <button type="button" class="btn-secondary" @click="cancelEdit">Cancel</button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.head button {
  width: auto;
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}
/* The same pill switch as the allocation block's "By holding / By market". */
.modes {
  display: flex;
  gap: 4px;
}
.mode {
  padding: 4px 10px;
  font-size: 12px;
  background: #f1f5f9;
  color: #475569;
  border: none;
  border-radius: 999px;
  cursor: pointer;
}
.mode.active {
  background: #0f172a;
  color: #fff;
}
.table-note {
  margin: 8px 0 0;
  font-size: 13px;
}
.sell {
  color: #b45309;
}
.trade-symbol {
  margin-left: 8px;
}
.intro {
  margin: 4px 0 12px;
  font-size: 14px;
}
.currency-plan {
  margin-top: 16px;
}
.currency-title {
  margin: 0 0 8px;
  font-size: 14px;
  letter-spacing: 0.04em;
}
.over {
  color: #b45309;
}
.under {
  color: #1d4ed8;
}
.notice {
  background: #fffbeb;
  border: 1px solid #fde68a;
  color: #92400e;
  padding: 10px 14px;
  border-radius: 8px;
  margin: 12px 0 0;
  font-size: 14px;
}
.planner {
  margin-top: 16px;
  padding: 14px 16px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
}
.planner-title {
  margin: 0 0 10px;
  font-size: 14px;
}
.planner-inputs {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 16px;
  margin-bottom: 8px;
}
.planner-inputs label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 13px;
  color: #475569;
}
.planner-inputs input[type='number'] {
  width: 160px;
}
.planner-inputs .checkbox {
  flex-direction: row;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
  padding-bottom: 8px;
}
/* The global input rule is full-width, which stretches a checkbox across the
   row and folds its label into a column. */
.planner-inputs .checkbox input {
  width: auto;
  margin: 0;
}
.planner-note {
  margin: 8px 0 0;
  font-size: 13px;
}
.planner-lead {
  margin: -4px 0 10px;
  font-size: 13px;
}
.planner-inputs select {
  width: 160px;
}
.planner + .planner {
  margin-top: 12px;
}
.edit-head {
  display: flex;
  align-items: baseline;
  gap: 12px;
}
.edit-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.edit-list li {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
}
.edit-symbol {
  min-width: 70px;
}
.edit-list input {
  width: 100px;
}
.sum {
  margin: 6px 0 0;
  font-size: 13px;
  font-variant-numeric: tabular-nums;
}
.sum.ok {
  color: #047857;
}
.sum.off {
  color: #b91c1c;
}
.add-field {
  margin-top: 16px;
}
.actions {
  display: flex;
  gap: 8px;
  margin-top: 12px;
}
.actions button {
  width: auto;
}
.link-button {
  background: none;
  border: none;
  padding: 0;
  color: #0d9488;
  text-decoration: underline;
  cursor: pointer;
  font-size: 13px;
  width: auto;
}
</style>
