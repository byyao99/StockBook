<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { instrumentApi, reportApi } from '../api/client'
import {
  UNKNOWN,
  formatBpsMagnitudeOrUnknown,
  formatBpsOrUnknown,
  formatCents,
  formatPercentOrUnknown,
  formatQty,
  formatSignedCents,
} from '../money'
import EquityCurveChart from '../components/EquityCurveChart.vue'
import type { CurrencyCurve, HindsightSummary, RealizedSummary, SyncResult } from '../types'

// How many years back the quick picker offers. A ledger older than this is
// still reachable through the date fields; the buttons are for the years
// somebody actually reaches for.
const YEARS_OFFERED = 5

const summaries = ref<RealizedSummary[]>([])
// What the same period's sales would be worth had the shares never been sold.
// It shares the period picker because it asks about the same entries; unlike
// the realized figures it is always measured against today's quote.
const hindsight = ref<HindsightSummary[]>([])
// The book's own daily history. It shares the picker too, but not on the same
// terms: the whole ledger is always folded, and the bounds only decide which
// sessions are drawn — narrowing to last month still shows holdings bought
// years ago, where the two reports below would drop them.
const curves = ref<CurrencyCurve[]>([])
const loading = ref(false)
const error = ref('')
const success = ref('')
const syncing = ref(false)
// Per-symbol history failures, shown for the same reason the quote refresh
// shows its own: a count with no symbol beside it is not actionable.
const syncResults = ref<SyncResult[]>([])

// The period, as the inclusive YYYY-MM-DD bounds the API takes. Empty means
// open-ended, which is how "All time" is expressed.
const from = ref('')
const to = ref('')

const thisYear = new Date().getFullYear()
const years = Array.from({ length: YEARS_OFFERED }, (_, i) => thisYear - i)

// What the current bounds describe, for the heading. A named year is worth
// saying as a year rather than as two dates.
const periodLabel = computed(() => {
  if (!from.value && !to.value) return 'All time'
  const year = matchedYear.value
  if (year !== null) return String(year)
  return `${from.value || 'the beginning'} to ${to.value || 'today'}`
})

const matchedYear = computed(() => {
  const match = years.find((y) => from.value === `${y}-01-01` && to.value === `${y}-12-31`)
  return match ?? null
})

function selectYear(year: number) {
  from.value = `${year}-01-01`
  to.value = `${year}-12-31`
  load()
}

function selectAllTime() {
  from.value = ''
  to.value = ''
  load()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [realized, sells, history] = await Promise.all([
      reportApi.realized(from.value || undefined, to.value || undefined),
      reportApi.hindsight(from.value || undefined, to.value || undefined),
      reportApi.curve(from.value || undefined, to.value || undefined),
    ])
    summaries.value = realized
    hindsight.value = sells
    curves.value = history
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

/**
 * Downloads the daily closes the chart is drawn from.
 *
 * The button lives beside the chart rather than with the quote refresh on
 * Holdings, because an empty chart is where somebody goes looking for it. A
 * first run downloads years and is genuinely slow; later ones top up a few
 * sessions each.
 */
async function syncHistory() {
  syncing.value = true
  error.value = ''
  success.value = ''
  syncResults.value = []
  try {
    const report = await instrumentApi.syncHistory()
    // Only the failures are worth listing: the server already leaves untraded
    // instruments out of `results`, and a successful sync speaks for itself
    // through the chart appearing.
    syncResults.value = report.results.filter((r) => r.status === 'failed')
    const added = report.results.reduce((sum, r) => sum + r.added, 0)
    const instruments = `${report.synced} ${report.synced === 1 ? 'instrument' : 'instruments'}`
    success.value = `Synced ${instruments}, ${added} ${added === 1 ? 'session' : 'sessions'} added.`
    if (report.failed > 0) {
      success.value += ` ${report.failed} could not be fetched — see below.`
    }
    await load()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    syncing.value = false
  }
}

/**
 * Return on the cost that was sold, as a fraction. Measured against the trading
 * result alone: dividends are earned by holding shares, not by selling them, so
 * counting them here would credit the sales with income they did not produce.
 * Null when nothing was sold — a period with no sales has no return, which is
 * not the same as 0%.
 */
function returnOnCost(s: RealizedSummary): number | null {
  if (s.cost === 0) return null
  return s.trading_pl / s.cost
}

/** "3 sales · 2 dividends", leaving out whichever the period has none of. */
function entryCounts(s: RealizedSummary): string {
  const parts: string[] = []
  if (s.sells > 0) parts.push(`${s.sells} ${s.sells === 1 ? 'sale' : 'sales'}`)
  if (s.dividend_count > 0) {
    parts.push(`${s.dividend_count} ${s.dividend_count === 1 ? 'dividend' : 'dividends'}`)
  }
  return parts.join(' · ')
}

function plClass(value: number): string {
  return value < 0 ? 'loss' : 'gain'
}

/**
 * What the difference between the sale and today's price means, in words. A
 * signed number alone does not say which way is good here: money left on the
 * table and money saved by getting out first are both worth naming.
 */
function sellingVerdict(gain: number): string {
  if (gain < 0) return 'left on the table by selling'
  if (gain > 0) return 'saved by selling before the fall'
  return 'exactly what those shares fetched'
}

/** "3 sales · 450 shares" for the period. */
function saleCounts(h: HindsightSummary): string {
  const sales = `${h.sells} ${h.sells === 1 ? 'sale' : 'sales'}`
  return `${sales} · ${formatQty(h.shares_sold)} shares`
}

/** "128 sessions · 4 holdings" for a curve. */
function curveCounts(c: CurrencyCurve): string {
  const sessions = `${formatQty(c.points.length)} ${c.points.length === 1 ? 'session' : 'sessions'}`
  return `${sessions} · ${c.instruments} ${c.instruments === 1 ? 'holding' : 'holdings'}`
}

// The year in progress is the one a user opens this page to look at.
onMounted(() => selectYear(thisYear))
</script>

<template>
  <div>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="success" class="success">{{ success }}</p>

    <section class="card period">
      <div class="years">
        <button
          v-for="y in years"
          :key="y"
          class="chip"
          :class="{ active: matchedYear === y }"
          @click="selectYear(y)"
        >
          {{ y }}
        </button>
        <button class="chip" :class="{ active: !from && !to }" @click="selectAllTime">
          All time
        </button>
      </div>
      <div class="range">
        <label>
          From
          <input v-model="from" type="date" @change="load" />
        </label>
        <label>
          To
          <input v-model="to" type="date" @change="load" />
        </label>
      </div>
    </section>

    <p v-if="loading" class="muted">Loading…</p>

    <!-- The book's own history, first: it is the only section here that shows
         holdings still open, and the two reports below both look backwards at
         entries that are already closed. -->
    <section v-if="!loading" class="history">
      <div class="section-head">
        <h2 class="report-title">History</h2>
        <button class="btn-secondary" :disabled="syncing" @click="syncHistory">
          {{ syncing ? 'Syncing prices…' : 'Sync prices' }}
        </button>
      </div>
      <p class="muted lede">
        What the book has been worth, session by session. The dates above choose which sessions are
        drawn — never what is counted, so a narrow window still includes holdings bought long
        before it.
      </p>

      <ul v-if="syncResults.length > 0" class="sync-log">
        <li v-for="r in syncResults" :key="r.instrument_id">
          <span class="badge badge-sell">failed</span>
          <strong>{{ r.symbol }}</strong>
          <span v-if="r.ticker" class="ticker">looked up as {{ r.ticker }}</span>
          <span class="muted">{{ r.error }}</span>
        </li>
      </ul>

      <p v-if="curves.length === 0" class="muted">
        There is no history to draw yet. Record a trade, then press Sync prices to download the
        daily closes the chart is built from.
      </p>

      <div v-for="c in curves" :key="c.currency" class="currency-block">
        <h3 class="currency-title">
          {{ c.currency }}
          <span class="muted">· {{ periodLabel }} · {{ curveCounts(c) }}</span>
        </h3>

        <!-- An empty curve explains itself in the server's own words. Drawing a
             flat line at zero instead would read as a book that went nowhere. -->
        <p v-if="c.points.length === 0" class="notice">
          {{ c.unavailable || 'There is nothing to draw for this period.' }}
        </p>

        <template v-else>
          <EquityCurveChart :curve="c" :currency="c.currency" />

          <div class="cards curve-stats">
            <div class="stat">
              <span class="stat-label">Time-weighted</span>
              <strong class="stat-value" :class="plClass(c.twr_bps ?? 0)">
                {{ formatBpsOrUnknown(c.twr_bps) }}
              </strong>
              <span class="muted stat-note">over this period, contributions divided out</span>
            </div>
            <div class="stat">
              <span class="stat-label">Annualized</span>
              <strong class="stat-value" :class="plClass(c.annualized_bps ?? 0)">
                {{ formatBpsOrUnknown(c.annualized_bps) }}
              </strong>
              <span class="muted stat-note">the same return stated as a yearly rate</span>
            </div>
            <div class="stat">
              <!-- A drawdown is a depth, not a movement: it is reported as a
                   positive number and must never render with a leading "+". -->
              <span class="stat-label">Max drawdown</span>
              <strong class="stat-value" :class="(c.max_drawdown_bps ?? 0) > 0 ? 'loss' : ''">
                {{ formatBpsMagnitudeOrUnknown(c.max_drawdown_bps) }}
              </strong>
              <span class="muted stat-note">deepest fall below its own high-water mark</span>
            </div>
          </div>
        </template>

        <p v-if="c.without_history > 0" class="notice">
          {{ c.without_history }}
          {{ c.without_history === 1 ? 'holding is' : 'holdings are' }}
          left out of this curve — the stored prices do not reach back to when
          {{ c.without_history === 1 ? 'it was' : 'they were' }} first traded, and counting
          {{ c.without_history === 1 ? 'it' : 'them' }} short would look like a real drawdown.
          Press Sync prices to fill the gap.
        </p>
      </div>
    </section>

    <h2 v-if="!loading" class="report-title realized-title">Realized</h2>
    <p v-if="!loading && summaries.length === 0" class="muted">
      Nothing was sold and no dividend was paid in this period, so there is nothing realized to
      report.
    </p>

    <!-- One block per currency, never a combined figure: there is no exchange
         rate in this system, so a TWD gain and a USD gain cannot be added. -->
    <section v-for="s in summaries" :key="s.currency" class="currency-block">
      <h2 class="currency-title">
        {{ s.currency }}
        <span class="muted">· {{ periodLabel }} · {{ entryCounts(s) }}</span>
      </h2>

      <!-- Trading and dividends are shown apart as well as together: they are
           taxed differently, and a book that lost on price while making it back
           on income must not read as a flat year. -->
      <div class="cards">
        <div class="stat">
          <span class="stat-label">Realized</span>
          <strong class="stat-value" :class="plClass(s.realized_pl)">
            {{ formatSignedCents(s.realized_pl, s.currency) }}
          </strong>
          <span class="muted stat-note">trading and dividends together</span>
        </div>
        <div class="stat">
          <span class="stat-label">Trading</span>
          <strong class="stat-value" :class="plClass(s.trading_pl)">
            {{ formatSignedCents(s.trading_pl, s.currency) }}
          </strong>
          <span class="muted stat-note">
            {{ formatPercentOrUnknown(returnOnCost(s)) }} on the cost sold
          </span>
        </div>
        <div class="stat">
          <span class="stat-label">Dividends</span>
          <strong class="stat-value" :class="s.dividends === 0 ? '' : 'gain'">
            {{ formatCents(s.dividends, s.currency) }}
          </strong>
          <span class="muted stat-note">after tax withheld</span>
        </div>
        <div class="stat">
          <span class="stat-label">Proceeds</span>
          <strong class="stat-value">{{ formatCents(s.proceeds, s.currency) }}</strong>
          <span class="muted stat-note">
            against {{ formatCents(s.cost, s.currency) }} of cost sold
          </span>
        </div>
      </div>

      <p v-if="s.unstamped_entries > 0" class="notice">
        {{ s.unstamped_entries }}
        {{ s.unstamped_entries === 1 ? 'entry is' : 'entries are' }}
        missing a computed result and {{ s.unstamped_entries === 1 ? 'is' : 'are' }} left out of
        these totals, so the period is not fully accounted for. This means the holding behind
        {{ s.unstamped_entries === 1 ? 'it' : 'them' }} could not be replayed — check the ledger for
        a buy that is missing.
      </p>

      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Symbol</th>
              <th class="num">Sales</th>
              <th class="num">Proceeds</th>
              <th class="num">Cost sold</th>
              <th class="num">Trading</th>
              <th class="num">Dividends</th>
              <th class="num">Realized</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in s.instruments" :key="row.instrument_id">
              <td>
                <strong>{{ row.symbol }}</strong>
                <div class="muted">{{ row.name }}</div>
              </td>
              <td class="num">{{ formatQty(row.sells) }}</td>
              <td class="num">{{ formatCents(row.proceeds, row.currency) }}</td>
              <td class="num">{{ formatCents(row.cost, row.currency) }}</td>
              <td class="num" :class="row.sells === 0 ? 'muted' : plClass(row.trading_pl)">
                <!-- A holding that only paid a dividend has no trading result;
                     a dash says so, where a signed zero would claim it broke
                     even on a sale it never made. -->
                {{ row.sells === 0 ? UNKNOWN : formatSignedCents(row.trading_pl, row.currency) }}
              </td>
              <td class="num" :class="row.dividends === 0 ? 'muted' : 'gain'">
                {{ row.dividend_count === 0 ? UNKNOWN : formatCents(row.dividends, row.currency) }}
              </td>
              <td class="num" :class="plClass(row.realized_pl)">
                {{ formatSignedCents(row.realized_pl, row.currency) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- What the same period's sales would be worth if they had never been
         made. Every broker shows what a sale banked; almost none shows what it
         gave up. -->
    <section v-if="!loading && hindsight.length > 0" class="hindsight">
      <h2 class="report-title">If you had never sold</h2>
      <p class="muted lede">
        What the sales in this period fetched, against what those same shares would be worth at
        the latest quote. It does not follow the money: cash from a sale usually buys something
        else, and this book has no cash balance to trace it into — so read a row where shares are
        still held as a round trip rather than as a loss taken.
      </p>

      <div v-for="h in hindsight" :key="h.currency" class="currency-block">
        <h3 class="currency-title">
          {{ h.currency }}
          <span class="muted">· {{ periodLabel }} · {{ saleCounts(h) }}</span>
        </h3>

        <div class="cards">
          <div class="stat">
            <span class="stat-label">Sales fetched</span>
            <strong class="stat-value">{{ formatCents(h.proceeds, h.currency) }}</strong>
            <span class="muted stat-note">after selling fees</span>
          </div>
          <div class="stat">
            <span class="stat-label">Worth today</span>
            <strong class="stat-value">{{ formatCents(h.value_if_held, h.currency) }}</strong>
            <span class="muted stat-note">the same shares at the latest quote</span>
          </div>
          <div class="stat">
            <span class="stat-label">Difference</span>
            <strong class="stat-value" :class="plClass(h.selling_gain)">
              {{ formatSignedCents(h.selling_gain, h.currency) }}
            </strong>
            <span class="muted stat-note">{{ sellingVerdict(h.selling_gain) }}</span>
          </div>
        </div>

        <p v-if="h.unpriced_sales > 0" class="notice">
          {{ h.unpriced_sales }} {{ h.unpriced_sales === 1 ? 'sale is' : 'sales are' }} left out:
          {{ h.unpriced_sales === 1 ? 'its' : 'their' }} instrument has no quote, so there is
          nothing to compare against. Refresh quotes on the Holdings page.
        </p>

        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Symbol</th>
                <th class="num">Sales</th>
                <th class="num">Shares sold</th>
                <th class="num">Fetched</th>
                <th class="num">Last price</th>
                <th class="num">Worth today</th>
                <th class="num">Difference</th>
                <th class="num">Still held</th>
              </tr>
            </thead>
            <tbody>
              <!-- Worst decision first: the sales that cost the most are what
                   there is to learn from. -->
              <tr v-for="row in h.instruments" :key="row.instrument_id">
                <td>
                  <strong>{{ row.symbol }}</strong>
                  <div class="muted">{{ row.name }}</div>
                </td>
                <td class="num">{{ formatQty(row.sells) }}</td>
                <td class="num">{{ formatQty(row.shares_sold) }}</td>
                <td class="num">{{ formatCents(row.proceeds, row.currency) }}</td>
                <td class="num">{{ formatCents(row.last_price, row.currency) }}</td>
                <td class="num">{{ formatCents(row.value_if_held, row.currency) }}</td>
                <td class="num" :class="plClass(row.selling_gain)">
                  {{ formatSignedCents(row.selling_gain, row.currency) }}
                </td>
                <!-- Shares still held mean the position was re-entered, so the
                     difference beside it was never actually given up. -->
                <td class="num" :class="row.shares_held === 0 ? 'muted' : ''">
                  {{ row.shares_held === 0 ? UNKNOWN : formatQty(row.shares_held) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.period {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}
.years {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.chip {
  width: auto;
  background: #f1f5f9;
  border: 1px solid #e2e8f0;
  color: #334155;
  padding: 6px 14px;
  border-radius: 999px;
  cursor: pointer;
  font-weight: 500;
  font-size: 14px;
}
.chip:hover {
  background: #e2e8f0;
}
.chip.active {
  background: #0d9488;
  border-color: #0d9488;
  color: #fff;
}
.range {
  display: flex;
  gap: 12px;
}
.range label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: #6b7280;
}
.range input {
  width: auto;
}
.currency-block {
  margin-bottom: 24px;
}
/* Three sections share this page, so each says which one it is. */
.report-title {
  font-size: 18px;
  font-weight: 600;
  color: #0f172a;
  margin: 0 0 10px;
}
.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}
.section-head .report-title {
  margin: 0;
}
.section-head button {
  width: auto;
}
.history {
  margin-bottom: 32px;
}
/* The chart's own figures sit under it rather than beside it: they describe the
   line above, and a reader arrives at them by looking down from it. */
.curve-stats {
  margin-top: 12px;
}
.sync-log {
  list-style: none;
  padding: 0;
  margin: 12px 0 0;
  font-size: 13px;
}
.sync-log li {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 6px 0;
  border-bottom: 1px solid #f1f5f9;
}
.ticker {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  color: #475569;
}
.realized-title {
  margin-top: 32px;
  border-top: 1px solid #e2e8f0;
  padding-top: 20px;
}
.hindsight {
  margin-top: 32px;
  border-top: 1px solid #e2e8f0;
  padding-top: 20px;
}
.lede {
  font-size: 13px;
  line-height: 1.6;
  max-width: 68ch;
  margin: 0 0 16px;
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
/* Wide tables scroll inside their own box rather than the page. */
.table-wrap {
  overflow-x: auto;
  background: #fff;
  border-radius: 12px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
  margin-top: 12px;
  padding: 4px 16px;
}
</style>
