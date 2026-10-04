<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { instrumentApi, positionApi, reportApi } from '../api/client'
import { claimAutoRun } from '../autoRefresh'
import {
  formatBpsOrUnknown,
  formatCents,
  formatCentsOrUnknown,
  formatPercentMagnitudeOrUnknown,
  formatPercentOrUnknown,
  formatSignedCents,
  formatSignedOrUnknown,
} from '../money'
import { formatShares } from '../shares'
import {
  averageCost,
  isUnpriced,
  portfolioWeight,
  returnPct,
  summaryReturnPct,
  unpricedCount,
} from '../positionMath'
import { byAssetType, byHolding, byMarket, topWeight } from '../allocation'
import type { Allocation } from '../allocation'
import PaginationBar from '../components/PaginationBar.vue'
import SplitWarnings from '../components/SplitWarnings.vue'
import type {
  Currency,
  CurrencySummary,
  Position,
  RefreshResult,
  ReturnsSummary,
  UnadjustedSplit,
} from '../types'

const PAGE_SIZE = 20

const positions = ref<Position[]>([])
// One summary per currency held. They are never combined into a grand total:
// there is no FX rate in this system, so adding TWD to USD would produce a
// number that means nothing.
const summaries = ref<CurrencySummary[]>([])
// The annualized money-weighted return, also one per currency. It belongs on
// this page rather than under Realized: its closing figure is the market value
// of what is still held, which is exactly what the cards here already show.
const returns = ref<ReturnsSummary[]>([])
const total = ref(0)
const offset = ref(0)
const includeClosed = ref(false)
const loading = ref(false)
const refreshing = ref(false)
const error = ref('')
const success = ref('')
// Per-symbol refresh failures. They used to live on a separate instruments
// page; with quotes managed from here they have to be shown here, or a failed
// fetch would report a count with no way to see which symbol it was.
const refreshResults = ref<RefreshResult[]>([])
// Splits this book held shares across. Not a prompt like the pending dividends
// on the Ledger page — there is no entry that settles one — but a warning that
// the figures on this very page are out by the ratio until the entries behind
// them are restated.
const splits = ref<UnadjustedSplit[]>([])
// Every open holding, which is not the same as the page above. An allocation
// computed from one page would claim weights over a partial book, and would be
// most wrong exactly when it matters — a concentrated position sitting on page
// two would simply not appear.
const allOpen = ref<Position[]>([])
// Which way the book is sliced. One control for every currency block: a reader
// comparing two books wants them cut the same way, and a per-block setting
// would let them drift apart without saying so.
const allocationMode = ref<'holding' | 'market' | 'type'>('holding')

/**
 * Fetch every open holding by paging to the end.
 *
 * The list endpoint caps a page at 100, so a book larger than that would
 * silently allocate over a prefix. Paging is a few requests at worst and is
 * always right, which is the trade this system makes everywhere else.
 */
async function loadAllOpen(): Promise<Position[]> {
  const items: Position[] = []
  const pageSize = 100
  for (;;) {
    const page = await positionApi.list(pageSize, items.length, false)
    items.push(...page.items)
    if (items.length >= page.pagination.total || page.items.length === 0) return items
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [page, totals, rates, open, unadjusted] = await Promise.all([
      positionApi.list(PAGE_SIZE, offset.value, includeClosed.value),
      positionApi.summary(),
      reportApi.returns(),
      loadAllOpen(),
      reportApi.splits(),
    ])
    positions.value = page.items
    total.value = page.pagination.total
    summaries.value = totals
    returns.value = rates
    allOpen.value = open
    splits.value = unadjusted
    // If a filter change emptied the current page, step back one.
    if (positions.value.length === 0 && offset.value > 0) {
      offset.value = Math.max(0, offset.value - PAGE_SIZE)
      await load()
    }
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

function changePage(newOffset: number) {
  offset.value = newOffset
  load()
}

// Pulling fresh quotes belongs on this page as much as on the instruments one:
// unrealized profit and loss is what this page is for, and it is only as
// current as the prices behind it. Quotes newer than a few minutes are left
// alone by the server, so pressing this repeatedly is cheap.
async function refreshQuotes({ automatic = false } = {}) {
  refreshing.value = true
  error.value = ''
  success.value = ''
  refreshResults.value = []
  try {
    const report = await instrumentApi.refreshQuotes()
    // Per-symbol failures are shown either way. They are the reason this is a
    // foreground action at all: a delisted ticker is only ever fixed by someone
    // reading the provider's own wording about it, and the automatic run has a
    // reader for exactly as long as this page is open.
    refreshResults.value = report.results.filter((r) => r.status !== 'updated')
    if (!automatic) {
      success.value = `Updated ${report.updated} ${report.updated === 1 ? 'quote' : 'quotes'}.`
      if (report.fresh > 0) {
        // Without this a no-op refresh looks like a button that does nothing.
        success.value += ` ${report.fresh} already current.`
      }
      if (report.failed > 0) {
        success.value += ` ${report.failed} could not be fetched — see below.`
      }
    }
    await load()
  } catch (e) {
    // A run that could not start at all — rate-limited, provider unreachable,
    // fetching not configured. The button reports it, the automatic attempt
    // does not: nothing here claims to have succeeded, every price on the page
    // already carries its own age, and a red banner on arrival for something
    // the reader did not ask for teaches them to ignore the banner. Pressing
    // Refresh surfaces the real reason.
    if (!automatic) {
      error.value = (e as Error).message
    }
  } finally {
    refreshing.value = false
  }
}

// Toggling the closed filter restarts paging from the first page.
function toggleClosed() {
  offset.value = 0
  load()
}

/** Picks the gain/loss class, or nothing at all when the value is unknown. */
// A holding's share of its own currency's priced book. Null — rendered as an
// em dash — when it has no quote: a weight of 0% would claim the position is
// negligible, which is the opposite of unknown.
function weightOf(p: Position): number | null {
  const block = summaries.value.find((s) => s.currency === p.currency)
  return block === undefined ? null : portfolioWeight(p, block)
}

function plClass(value: number | null): string {
  if (value === null) return 'muted'
  return value < 0 ? 'loss' : 'gain'
}

/**
 * How each currency's book is divided up, keyed by currency.
 *
 * Per currency because there is no exchange rate here: a TWD holding's share of
 * a book that also holds USD is not a number that exists. Unpriced holdings are
 * excluded rather than counted as zero — a slice of 0% would claim a position
 * is negligible when it may be the largest one — and the count comes back so
 * the block can say what share of itself the picture accounts for.
 *
 * Computed once per currency rather than called from the template, which reads
 * it five times per block.
 */
const allocations = computed(() => {
  const slice = (held: Position[]) => {
    switch (allocationMode.value) {
      case 'market':
        return byMarket(held)
      case 'type':
        return byAssetType(held)
      default:
        return byHolding(held)
    }
  }
  const map = new Map<Currency, Allocation>()
  for (const s of summaries.value) {
    map.set(
      s.currency,
      slice(allOpen.value.filter((p) => p.currency === s.currency)),
    )
  }
  return map
})

/** One currency's allocation, empty when nothing in it can be valued. */
function allocationFor(currency: Currency): Allocation {
  return allocations.value.get(currency) ?? { slices: [], total: 0, unpriced: 0 }
}

/**
 * The combined weight of the three largest slices — "the top three are 62% of
 * this book". Three because it is the smallest number that says something about
 * a book rather than about one position, which the largest slice already does.
 */
function topThree(allocation: Allocation): number | null {
  return topWeight(allocation, 3)
}

/**
 * A deterministic colour per slice, walked round a fixed palette.
 *
 * The palette deliberately avoids the green and red this page uses for gains
 * and losses: a wedge is a size, not a result, and colouring one red would say
 * something about it that is not true.
 */
const SLICE_COLOURS = [
  '#0d9488',
  '#0ea5e9',
  '#6366f1',
  '#a855f7',
  '#f59e0b',
  '#64748b',
  '#0891b2',
  '#7c3aed',
]
function sliceColour(index: number): string {
  return SLICE_COLOURS[index % SLICE_COLOURS.length]
}

const returnsByCurrency = computed(() => {
  const map = new Map<Currency, ReturnsSummary>()
  for (const r of returns.value) map.set(r.currency, r)
  return map
})

/** The annualized rate for a currency in basis points, null when there is none. */
function rateFor(currency: Currency): number | null {
  return returnsByCurrency.value.get(currency)?.xirr_bps ?? null
}

/**
 * What to say under the rate: the period it averages over, or — when the server
 * could not compute one — its reason, in the server's own words. An absent rate
 * has to explain itself, because the alternative reading of a blank figure is
 * that the book went nowhere.
 */
function rateNote(currency: Currency): string {
  const r = returnsByCurrency.value.get(currency)
  if (!r) return ''
  if (r.xirr_bps === null) return r.unavailable ?? 'not enough history to measure'
  // Dates are read off the UTC timestamp, the same way the ledger shows them.
  const since = r.first_flow_at?.slice(0, 10)
  return since ? `per year since ${since}` : 'per year'
}

// Load first, then refresh: the stored book renders immediately and the fresh
// prices land a moment later, rather than the page waiting on a provider.
//
// This is not the background ticker CLAUDE.md rules out. That one is refused
// because a failed symbol lands in a log nobody reads; this one runs only while
// somebody is looking at the page it reports into. The server's own freshness
// window does the deciding, so arriving twice in an afternoon costs one round
// of provider traffic, and autoRefreshGuard keeps navigation churn from even
// asking.
onMounted(async () => {
  await load()
  // 60s is far shorter than the server's 15-minute quote window, so this can
  // only ever suppress an ask that would have been told "all current".
  if (claimAutoRun('quotes', 60_000)) {
    await refreshQuotes({ automatic: true })
  }
})
</script>

<template>
  <div>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="success" class="success">{{ success }}</p>

    <div class="page-actions">
      <button class="btn-secondary" :disabled="refreshing" @click="refreshQuotes()">
        {{ refreshing ? 'Fetching quotes…' : 'Refresh quotes' }}
      </button>
    </div>

    <!-- Above everything, because what it says is that the figures below are
         wrong. It is deliberately not folded into a currency block: a reader
         scanning the cards has to meet it before they believe one. -->
    <SplitWarnings :splits="splits" show-symbol @changed="load" />

    <ul v-if="refreshResults.length > 0" class="refresh-log">
      <li v-for="r in refreshResults" :key="r.instrument_id">
        <span :class="['badge', r.status === 'failed' ? 'badge-sell' : 'badge-warn']">
          {{ r.status }}
        </span>
        <strong>{{ r.symbol }}</strong>
        <span v-if="r.ticker" class="ticker">looked up as {{ r.ticker }}</span>
        <span class="muted">{{ r.error }}</span>
      </li>
    </ul>

    <!-- One block per currency. Side by side rather than summed, because a
         combined figure would require an exchange rate this system does not
         have and would silently invent. -->
    <section v-for="s in summaries" :key="s.currency" class="currency-block">
      <h2 class="currency-title">
        {{ s.currency }}
        <span class="muted">· {{ s.open_positions }} open</span>
      </h2>
      <div class="cards">
        <div class="stat">
          <span class="stat-label">Cost basis</span>
          <strong class="stat-value">{{ formatCents(s.total_cost_basis, s.currency) }}</strong>
        </div>
        <div class="stat">
          <span class="stat-label">Market value</span>
          <strong class="stat-value">{{ formatCents(s.total_market_value, s.currency) }}</strong>
          <span v-if="unpricedCount(s) > 0" class="badge badge-warn">
            {{ unpricedCount(s) }} unpriced
          </span>
          <span v-else class="muted stat-note">all holdings priced</span>
        </div>
        <div class="stat">
          <span class="stat-label">Unrealized</span>
          <strong class="stat-value" :class="plClass(s.total_unrealized_pl)">
            {{ formatSignedCents(s.total_unrealized_pl, s.currency) }}
          </strong>
          <span class="muted stat-note">
            {{ formatPercentOrUnknown(summaryReturnPct(s)) }} on priced cost
          </span>
        </div>
        <div class="stat">
          <span class="stat-label">Realized</span>
          <strong class="stat-value" :class="plClass(s.total_realized_pl)">
            {{ formatSignedCents(s.total_realized_pl, s.currency) }}
          </strong>
          <span class="muted stat-note">banked, all time — dividends included</span>
        </div>
        <!-- The one figure a plain gain-over-cost percentage cannot give: it
             weights every dollar by how long it was actually at work, so a book
             is comparable with another book, or with a savings rate. -->
        <div class="stat">
          <span class="stat-label">Annualized (XIRR)</span>
          <strong class="stat-value" :class="plClass(rateFor(s.currency))">
            {{ formatBpsOrUnknown(rateFor(s.currency)) }}
          </strong>
          <span class="muted stat-note">{{ rateNote(s.currency) }}</span>
        </div>
      </div>
      <p v-if="unpricedCount(s) > 0" class="notice">
        {{ unpricedCount(s) }} open {{ unpricedCount(s) === 1 ? 'holding has' : 'holdings have' }}
        no quote, so the {{ s.currency }} market value and unrealized figures exclude
        {{ unpricedCount(s) === 1 ? 'it' : 'them' }}, and the annualized return leaves
        {{ unpricedCount(s) === 1 ? 'its' : 'their' }} history out entirely — money paid in with
        no known value to show for it would read as a total loss. Try Refresh quotes above.
      </p>

      <!-- Every other figure on this page is about profit. This one is about
           risk, and it is the question a ledger can answer exactly while a
           broker statement usually will not. -->
      <div v-if="allocationFor(s.currency).slices.length > 0" class="allocation">
        <div class="allocation-head">
          <span class="allocation-title">
            Allocation
            <span class="muted">
              · top 3 is {{ formatPercentMagnitudeOrUnknown(topThree(allocationFor(s.currency))) }}
            </span>
          </span>
          <div class="modes">
            <button
              v-for="m in [
                { key: 'holding', label: 'By holding' },
                { key: 'market', label: 'By market' },
                { key: 'type', label: 'By type' },
              ]"
              :key="m.key"
              class="mode"
              :class="{ active: allocationMode === m.key }"
              @click="allocationMode = m.key as typeof allocationMode"
            >
              {{ m.label }}
            </button>
          </div>
        </div>

        <div class="bar">
          <span
            v-for="(slice, i) in allocationFor(s.currency).slices"
            :key="slice.key"
            class="bar-slice"
            :style="{ width: `${slice.weight * 100}%`, background: sliceColour(i) }"
            :title="`${slice.label} ${formatPercentMagnitudeOrUnknown(slice.weight)}`"
          />
        </div>

        <ul class="legend">
          <li v-for="(slice, i) in allocationFor(s.currency).slices" :key="slice.key">
            <span class="swatch" :style="{ background: sliceColour(i) }" />
            <span class="legend-label">{{ slice.label }}</span>
            <span class="legend-weight">{{ formatPercentMagnitudeOrUnknown(slice.weight) }}</span>
            <span class="muted legend-value">{{ formatCents(slice.value, s.currency) }}</span>
          </li>
        </ul>

        <!-- The same rule the weight column follows: an unpriced holding is
             left out rather than drawn as a sliver, because its value is
             unknown and could be the largest position here. -->
        <p v-if="allocationFor(s.currency).unpriced > 0" class="muted allocation-note">
          {{ allocationFor(s.currency).unpriced }}
          {{ allocationFor(s.currency).unpriced === 1 ? 'holding is' : 'holdings are' }}
          not in this picture for want of a quote.
        </p>
      </div>
    </section>

    <section class="card">
      <div class="head">
        <h2 class="section-title">Holdings ({{ total }})</h2>
        <label class="toggle">
          <input v-model="includeClosed" type="checkbox" @change="toggleClosed" />
          Show closed
        </label>
      </div>

      <p v-if="loading" class="muted">Loading…</p>
      <p v-else-if="positions.length === 0" class="muted">
        No holdings yet — add a trade on the Ledger page.
      </p>
      <div v-else class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Symbol</th>
              <th>Ccy</th>
              <th class="num">Shares</th>
              <th class="num">Avg cost</th>
              <th class="num">Cost basis</th>
              <th class="num">Last price</th>
              <th class="num">Market value</th>
              <th class="num">Weight</th>
              <th class="num">Unrealized</th>
              <th class="num">Return</th>
              <th class="num">Realized</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in positions" :key="p.id" :class="{ closed: p.quantity === 0 }">
              <td>
                <!-- The symbol links to the holding's own page, which is where
                     its history, its own rate of return and its link onward to
                     the research feed all live. A closed holding links too: its
                     entries and what it banked are exactly what a reader is
                     looking for once it is sold. -->
                <RouterLink
                  :to="{ name: 'holding', params: { id: p.instrument_id } }"
                  class="symbol-link"
                >
                  {{ p.symbol }}
                </RouterLink>
                <div class="muted">{{ p.name }}</div>
              </td>
              <td class="muted">{{ p.currency }}</td>
              <td class="num">{{ formatShares(p.quantity) }}</td>
              <td class="num">{{ formatCentsOrUnknown(averageCost(p), p.currency) }}</td>
              <td class="num">{{ formatCents(p.cost_basis, p.currency) }}</td>
              <td class="num">
                {{ formatCentsOrUnknown(p.last_price, p.currency) }}
                <span v-if="isUnpriced(p)" class="badge badge-warn">no quote</span>
              </td>
              <td class="num">{{ formatCentsOrUnknown(p.market_value, p.currency) }}</td>
              <!-- Share of this holding's own currency book. There is no
                   exchange rate here, so a weight across currencies is not a
                   number that exists. -->
              <td class="num">{{ formatPercentMagnitudeOrUnknown(weightOf(p)) }}</td>
              <td class="num" :class="plClass(p.unrealized_pl)">
                {{ formatSignedOrUnknown(p.unrealized_pl, p.currency) }}
              </td>
              <td class="num" :class="plClass(p.unrealized_pl)">
                {{ formatPercentOrUnknown(returnPct(p)) }}
              </td>
              <td class="num" :class="plClass(p.realized_pl)">
                {{ formatSignedCents(p.realized_pl, p.currency) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <PaginationBar :limit="PAGE_SIZE" :offset="offset" :total="total" @change="changePage" />
    </section>
  </div>
</template>

<style scoped>
.page-actions {
  display: flex;
  justify-content: flex-end;
  margin-bottom: 12px;
}
.page-actions button {
  width: auto;
}
.refresh-log {
  list-style: none;
  margin: 0 0 16px;
  padding: 10px 14px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  font-size: 13px;
}
.refresh-log li {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
  flex-wrap: wrap;
}
.ticker {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  color: #475569;
  background: #e2e8f0;
  border-radius: 4px;
  padding: 1px 6px;
}
/* Dark text with a plainly visible underline, rather than the accent colour a
   link would usually take. Colour is already carrying meaning in this table —
   green is a gain and red is a loss — and the teal accent sits close enough to
   that green to be read as one, so a third meaning for it would dilute the two
   doing real work. The underline borrows nothing and is the oldest link
   convention there is. It has to be genuinely visible to do that job: a first
   attempt at #cbd5e1 read as the bold text it replaced. */
.symbol-link {
  color: #0f172a;
  font-weight: 700;
  text-decoration: underline;
  text-decoration-color: #94a3b8;
  text-decoration-thickness: 1px;
  text-underline-offset: 3px;
}
.symbol-link:hover {
  color: #0d9488;
  text-decoration-color: #0d9488;
}
.currency-block {
  margin-bottom: 20px;
}
.currency-title {
  font-size: 13px;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: #334155;
  margin: 0 0 8px;
}
.currency-title .muted {
  font-weight: 400;
  text-transform: none;
  letter-spacing: 0;
}
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
}
.stat {
  display: flex;
  flex-direction: column;
  gap: 4px;
  align-items: flex-start;
  background: #fff;
  border-radius: 12px;
  padding: 16px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
}
.stat-label {
  color: #6b7280;
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.stat-value {
  font-size: 22px;
  font-variant-numeric: tabular-nums;
}
.stat-note {
  font-size: 12px;
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
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.allocation {
  background: #fff;
  border-radius: 12px;
  padding: 14px 16px;
  margin-top: 12px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
}
.allocation-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}
.allocation-title {
  font-size: 12px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: #6b7280;
}
.allocation-title .muted {
  font-weight: 400;
  text-transform: none;
  letter-spacing: 0;
}
.modes {
  display: flex;
  gap: 4px;
}
.mode {
  width: auto;
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
/* One bar rather than a pie: a length is read accurately and an angle is not,
   and the whole point of this is comparing one wedge against another. */
.bar {
  display: flex;
  height: 14px;
  border-radius: 7px;
  overflow: hidden;
  background: #f1f5f9;
}
.bar-slice {
  height: 100%;
  min-width: 1px;
}
.legend {
  list-style: none;
  margin: 10px 0 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
  gap: 2px 16px;
  font-size: 13px;
}
.legend li {
  display: flex;
  align-items: center;
  gap: 8px;
}
.swatch {
  width: 10px;
  height: 10px;
  border-radius: 2px;
  flex-shrink: 0;
}
.legend-label {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.legend-weight {
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}
.legend-value {
  font-variant-numeric: tabular-nums;
  font-size: 12px;
}
.allocation-note {
  margin: 8px 0 0;
  font-size: 12px;
}
.toggle {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  color: #6b7280;
}
.toggle input {
  width: auto;
}
/* Wide tables scroll inside their own box rather than the page. */
.table-wrap {
  overflow-x: auto;
}
.closed td {
  opacity: 0.6;
}
</style>
