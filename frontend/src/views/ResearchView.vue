<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { positionApi, researchApi } from '../api/client'
import { UNKNOWN, formatCents, formatCompactCents, formatPercentOrUnknown } from '../money'
import { buildBars, latestReported, periodLabel, valueOf, yoyChange } from '../fundamentalsMath'
import { formatNewsTime } from '../newsTime'
import PaginationBar from '../components/PaginationBar.vue'
import type {
  FinancialMetric,
  FinancialPeriod,
  NewsArticle,
  Position,
  ResearchSyncReport,
} from '../types'

const PAGE_SIZE = 20

// The metrics the panel plots, and how each is written. Revenue and net income
// are compacted because they run to trillions; earnings per share is a few
// dollars and would read as "NT$0.00K".
const METRICS: { key: FinancialMetric; label: string; compact: boolean }[] = [
  { key: 'revenue', label: 'Revenue', compact: true },
  { key: 'net_income', label: 'Net income', compact: true },
  { key: 'diluted_eps', label: 'Diluted EPS', compact: false },
]

// Arriving from a holding on /positions means "tell me about this company", so
// the query param seeds BOTH controls rather than only the feed: a reader who
// clicked 2330 wants its headlines and its results, not its headlines beside
// somebody else's figures.
//
// It is a starting point, not a mirror. The controls are independent page state
// afterwards and the URL is not rewritten as they change — writing back would
// mean deciding which of the two independent selects owns the one parameter.
const route = useRoute()
const arrivedFor = typeof route.query.instrument_id === 'string' ? route.query.instrument_id : ''

const holdings = ref<Position[]>([])
const articles = ref<NewsArticle[]>([])
const total = ref(0)
const offset = ref(0)
const feedFilter = ref(arrivedFor)

const periods = ref<FinancialPeriod[]>([])
const selected = ref(arrivedFor)
const cadence = ref<'quarterly' | 'annual'>('quarterly')

const loading = ref(false)
const syncing = ref(false)
const error = ref('')
const success = ref('')
// Why the page is showing something other than what a link asked for. Not an
// error — nothing broke and the reader did nothing wrong — so it renders as a
// notice, like every other "here is why this is not what you expected" case.
const redirected = ref('')
// Per-source and per-instrument sync failures. A run that half worked has to say
// which half, or the counts are not actionable — the same reason the holdings
// page shows its refresh failures rather than only a number.
const syncLog = ref<{ label: string; status: string; detail: string }[]>([])

const selectedHolding = computed(() => holdings.value.find((h) => h.instrument_id === selected.value))

// The currency the figures were REPORTED in, which is not necessarily the one
// the shares trade in. Read off the periods rather than the holding, so the
// label is what the provider actually said.
const reportedCurrency = computed(() => periods.value[0]?.currency ?? null)
const currencyDiffers = computed(
  () =>
    reportedCurrency.value !== null &&
    selectedHolding.value !== undefined &&
    reportedCurrency.value !== selectedHolding.value.currency,
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [page, positions] = await Promise.all([
      researchApi.news(PAGE_SIZE, offset.value, feedFilter.value || undefined),
      positionApi.list(100, 0, false),
    ])
    articles.value = page.items
    total.value = page.pagination.total
    holdings.value = positions.items

    // A link can name a holding this page cannot show — one sold since the link
    // was made, or an id typed by hand. Falling back is better than leaving a
    // picker on a value no option carries, which renders as an unexplained
    // blank; saying so is better still, since the reader asked for something
    // specific and is about to be shown something else.
    // The feed above was already fetched under that filter, so clearing it is
    // not enough — the page would fall back to "all holdings" and show an empty
    // list. Fetch again with the filter gone. This cannot recurse: the second
    // pass finds an empty selection and skips the check.
    if (selected.value !== '' && !holdings.value.some((h) => h.instrument_id === selected.value)) {
      redirected.value =
        'The holding that link named is not open, so there is nothing here about it. ' +
        'Showing the rest of the book instead.'
      selected.value = ''
      feedFilter.value = ''
      await load()
      return
    }

    // Default the figures panel to the largest holding: it is the one whose
    // results matter most to this book.
    if (!selected.value && holdings.value.length > 0) {
      selected.value = largestHolding().instrument_id
    }
    // If a filter change emptied the current page, step back one.
    if (articles.value.length === 0 && offset.value > 0) {
      offset.value = Math.max(0, offset.value - PAGE_SIZE)
      await load()
      return
    }
    await loadFundamentals()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

// The holding with the most money in it, by market value where there is a quote
// and cost basis where there is not — an unpriced holding is not a small one.
function largestHolding(): Position {
  return holdings.value.reduce((biggest, candidate) =>
    (candidate.market_value ?? candidate.cost_basis) > (biggest.market_value ?? biggest.cost_basis)
      ? candidate
      : biggest,
  )
}

async function loadFundamentals() {
  if (!selected.value) {
    periods.value = []
    return
  }
  periods.value = await researchApi.fundamentals(selected.value, cadence.value)
}

async function reloadFundamentals() {
  error.value = ''
  try {
    await loadFundamentals()
  } catch (e) {
    error.value = (e as Error).message
  }
}

function changePage(newOffset: number) {
  offset.value = newOffset
  load()
}

function applyFilter() {
  offset.value = 0
  redirected.value = ''
  load()
}

watch([selected, cadence], reloadFundamentals)

async function sync() {
  syncing.value = true
  error.value = ''
  success.value = ''
  syncLog.value = []
  try {
    const report = await researchApi.sync()
    success.value = describe(report)
    syncLog.value = [
      ...report.news.sources
        .filter((s) => s.status !== 'synced')
        .map((s) => ({ label: s.scope, status: s.status, detail: s.error ?? '' })),
      ...report.fundamentals.results
        .filter((r) => r.status !== 'synced')
        .map((r) => ({ label: r.symbol, status: r.status, detail: r.error ?? '' })),
    ]
    await load()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    syncing.value = false
  }
}

// What a run did, in a sentence. Saying nothing when a press changed nothing
// would make the button look broken, so the skipped-as-fresh counts are part of
// the report rather than hidden.
function describe(report: ResearchSyncReport): string {
  const parts = [
    `Collected ${report.news.collected} ${report.news.collected === 1 ? 'article' : 'articles'}.`,
  ]
  if (report.news.fresh > 0) {
    parts.push(`${report.news.fresh} already checked recently.`)
  }
  if (report.news.error) parts.push(report.news.error)
  if (report.fundamentals.synced > 0) {
    parts.push(`Updated figures for ${report.fundamentals.synced}.`)
  }
  if (report.fundamentals.failed > 0) {
    parts.push(`${report.fundamentals.failed} could not be fetched — see below.`)
  }
  return parts.join(' ')
}

// How many periods the chart draws. The table below carries the whole record;
// the chart carries the recent shape, and a window is what keeps a label under
// every bar — six years of quarters is twenty-four of them, which no axis this
// wide can name.
const PLOT_PERIODS = 8

// The bars for one metric, with a baseline in the middle when anything in the
// run is negative. A loss drawn upward from the floor next to a profit would
// read as a good quarter.
//
// Bars are measured from zero, not from the smallest figure in the run. A
// truncated baseline would turn TSMC's real 36% year on the revenue chart into
// a cliff, which is the oldest way there is to lie with a bar chart. The growth
// the bars understate is stated outright beside them instead.
//
// Each bar is a percentage of its own half of the track, so the track's height
// stays a CSS concern and no percentage has to resolve against the wrong axis.
function plot(metric: FinancialMetric) {
  const bars = buildBars(periods.value.slice(-PLOT_PERIODS), metric)
  const signed = bars.some((bar) => bar.negative)
  return bars.map((bar) => ({ ...bar, size: bar.height * 100, signed }))
}

function formatValue(value: number | null, metric: FinancialMetric): string {
  if (value === null) return UNKNOWN
  const currency = reportedCurrency.value ?? undefined
  const spec = METRICS.find((m) => m.key === metric)
  return spec?.compact ? formatCompactCents(value, currency) : formatCents(value, currency)
}

function headline(metric: FinancialMetric): string {
  const period = latestReported(periods.value, metric)
  return period === null ? UNKNOWN : formatValue(valueOf(period, metric), metric)
}

function headlineNote(metric: FinancialMetric): string {
  const period = latestReported(periods.value, metric)
  if (period === null) return 'not reported'
  const change = yoyChange(periods.value, metric, periods.value.indexOf(period))
  return `${periodLabel(period)} · ${formatPercentOrUnknown(change)} year on year`
}

function changeClass(metric: FinancialMetric): string {
  const period = latestReported(periods.value, metric)
  if (period === null) return ''
  const change = yoyChange(periods.value, metric, periods.value.indexOf(period))
  if (change === null || change === 0) return ''
  return change > 0 ? 'gain' : 'loss'
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-actions">
      <span class="muted">
        {{ holdings.length }} {{ holdings.length === 1 ? 'holding' : 'holdings' }}
      </span>
      <button class="btn-secondary" :disabled="syncing || holdings.length === 0" @click="sync">
        {{ syncing ? 'Syncing…' : 'Sync' }}
      </button>
    </div>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="success" class="success">{{ success }}</p>
    <p v-if="redirected" class="notice">{{ redirected }}</p>

    <ul v-if="syncLog.length > 0" class="sync-log">
      <li v-for="entry in syncLog" :key="entry.label + entry.status">
        <span :class="['badge', entry.status === 'failed' ? 'badge-sell' : 'badge-warn']">
          {{ entry.status }}
        </span>
        <strong>{{ entry.label }}</strong>
        <span class="muted">{{ entry.detail }}</span>
      </li>
    </ul>

    <section class="card">
      <div class="section-head">
        <h2 class="section-title">Feed</h2>
        <select v-model="feedFilter" class="picker" @change="applyFilter">
          <option value="">All holdings</option>
          <option v-for="h in holdings" :key="h.instrument_id" :value="h.instrument_id">
            {{ h.symbol }} — {{ h.name }}
          </option>
        </select>
      </div>

      <!-- The limit is worth stating plainly rather than leaving a reader to
           wonder why a busy week looks quiet. An article reaches this list only
           because the source itself tagged it with a holding's code; nothing is
           matched on the company's name, which is what keeps somebody else's
           news out of it. -->
      <p class="notice">
        Only articles the source tagged with one of your holdings appear here, so a
        company may have news that never reaches this feed. Nothing is matched on a
        company's name.
      </p>

      <p v-if="loading" class="muted">Loading…</p>
      <p v-else-if="articles.length === 0" class="muted">
        No headlines yet. Press Sync to fetch them.
      </p>

      <ul v-else class="feed">
        <li v-for="article in articles" :key="article.id" class="feed-item">
          <div class="feed-tickers">
            <span v-for="symbol in article.symbols" :key="symbol" class="badge badge-buy">
              {{ symbol }}
            </span>
          </div>
          <div class="feed-body">
            <a :href="article.url" target="_blank" rel="noopener noreferrer" class="feed-title">
              {{ article.title }}
            </a>
            <p v-if="article.summary" class="feed-summary muted">{{ article.summary }}</p>
            <p class="feed-meta muted">
              {{ article.publisher }} · {{ formatNewsTime(article.published_at) }}
              <span class="ticker">{{ article.language.toUpperCase() }}</span>
            </p>
          </div>
        </li>
      </ul>

      <PaginationBar
        :limit="PAGE_SIZE"
        :offset="offset"
        :total="total"
        @change="changePage"
      />
    </section>

    <section class="card">
      <div class="section-head">
        <h2 class="section-title">Financials</h2>
        <div class="controls">
          <button
            v-for="option in (['quarterly', 'annual'] as const)"
            :key="option"
            :class="['chip', { active: cadence === option }]"
            @click="cadence = option"
          >
            {{ option === 'quarterly' ? 'Quarterly' : 'Annual' }}
          </button>
          <select v-model="selected" class="picker">
            <option v-for="h in holdings" :key="h.instrument_id" :value="h.instrument_id">
              {{ h.symbol }} — {{ h.name }}
            </option>
          </select>
        </div>
      </div>

      <p v-if="holdings.length === 0" class="muted">
        Nothing held, so there are no results to show.
      </p>
      <p v-else-if="periods.length === 0" class="muted">
        No reported figures yet. Press Sync to fetch them.
      </p>

      <template v-else>
        <!-- A company can report in a currency its shares do not trade in — an
             ADR is the ordinary case. These figures are never added to anything
             on this book, so the mismatch is a thing to say rather than an
             error, but it has to be said or the reader will assume otherwise. -->
        <p v-if="currencyDiffers" class="notice">
          {{ selectedHolding?.symbol }} reports in {{ reportedCurrency }} while its shares
          trade in {{ selectedHolding?.currency }}. These figures are in
          {{ reportedCurrency }} and are not converted.
        </p>

        <div v-for="metric in METRICS" :key="metric.key" class="metric">
          <div class="metric-head">
            <span class="stat-label">{{ metric.label }}</span>
            <strong class="stat-value">{{ headline(metric.key) }}</strong>
            <span :class="['muted', 'stat-note', changeClass(metric.key)]">
              {{ headlineNote(metric.key) }}
            </span>
          </div>
          <div class="bars">
            <div
              v-for="(bar, index) in plot(metric.key)"
              :key="index"
              class="bar-col"
              :title="`${periodLabel(bar.period)}: ${formatValue(bar.value, metric.key)}`"
            >
              <!-- Two halves about a baseline. A period with no figure draws
                   nothing in either: a zero-height bar sitting on the axis would
                   read as a quarter the company earned nothing in, which is not
                   what a missing figure means. The label under it is what makes
                   that gap say *which* quarter went unreported. -->
              <div class="track" :class="{ signed: bar.signed }">
                <span class="half above">
                  <span
                    v-if="bar.value !== null && !bar.negative"
                    class="bar bar-positive"
                    :style="{ height: bar.size + '%' }"
                  />
                </span>
                <span class="half below">
                  <span
                    v-if="bar.negative"
                    class="bar bar-negative"
                    :style="{ height: bar.size + '%' }"
                  />
                </span>
              </div>
              <span class="bar-label" :class="{ missing: bar.value === null }">
                {{ periodLabel(bar.period) }}
              </span>
            </div>
          </div>
        </div>

        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Period</th>
                <th class="num">Revenue</th>
                <th class="num">Net income</th>
                <th class="num">Diluted EPS</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="period in [...periods].reverse()" :key="period.as_of_date">
                <td>{{ periodLabel(period) }}</td>
                <td class="num">{{ formatValue(period.revenue, 'revenue') }}</td>
                <td class="num">{{ formatValue(period.net_income, 'net_income') }}</td>
                <td class="num">{{ formatValue(period.diluted_eps, 'diluted_eps') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
.page-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  margin-bottom: 12px;
}
.page-actions button {
  width: auto;
}
.sync-log {
  list-style: none;
  margin: 0 0 16px;
  padding: 10px 14px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  font-size: 13px;
}
.sync-log li {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
  flex-wrap: wrap;
}
.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.controls {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.picker {
  width: auto;
  max-width: 260px;
}
.chip {
  width: auto;
  padding: 4px 10px;
  font-size: 13px;
  border: 1px solid #e2e8f0;
  border-radius: 999px;
  background: #fff;
  cursor: pointer;
}
.chip.active {
  border-color: #0d9488;
  color: #0d9488;
  font-weight: 600;
}
.feed {
  list-style: none;
  margin: 0;
  padding: 0;
}
.feed-item {
  display: flex;
  gap: 12px;
  padding: 10px 0;
  border-bottom: 1px solid #f1f5f9;
}
.feed-item:last-child {
  border-bottom: none;
}
.feed-tickers {
  display: flex;
  flex-direction: column;
  gap: 4px;
  flex: 0 0 72px;
}
.feed-body {
  min-width: 0;
}
.feed-title {
  color: #0f172a;
  font-weight: 600;
  text-decoration: none;
}
.feed-title:hover {
  color: #0d9488;
  text-decoration: underline;
}
.feed-summary {
  margin: 4px 0 0;
  font-size: 13px;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.feed-meta {
  margin: 4px 0 0;
  font-size: 12px;
  display: flex;
  align-items: center;
  gap: 6px;
}
.ticker {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11px;
  color: #475569;
  background: #e2e8f0;
  border-radius: 4px;
  padding: 1px 6px;
}
.metric {
  margin-bottom: 16px;
}
.metric-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex-wrap: wrap;
}
.stat-label {
  color: #6b7280;
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.stat-value {
  font-size: 20px;
  font-variant-numeric: tabular-nums;
}
.stat-note {
  font-size: 12px;
}
.bars {
  display: flex;
  align-items: stretch;
  gap: 6px;
  margin-top: 6px;
}
.bar-col {
  flex: 1 1 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 4px;
}
.track {
  display: flex;
  flex-direction: column;
  height: 44px;
  border-bottom: 1px solid #e2e8f0;
}
.bar-label {
  font-size: 11px;
  color: #6b7280;
  text-align: center;
  white-space: nowrap;
}
/* A period the company did not report is named in the axis but greyed, so the
   gap above it reads as "not reported" rather than as a rendering fault. */
.bar-label.missing {
  color: #cbd5e1;
  font-style: italic;
}
/* The track is two halves about a baseline. With nothing negative in the run
   the lower half collapses and every bar grows from the floor; as soon as one
   figure is below zero the baseline moves to the middle, so a loss reads as
   below zero rather than as a short good quarter. */
.half {
  display: flex;
  flex: 1 1 0;
}
.half.above {
  align-items: flex-end;
}
.half.below {
  flex: 0 0 0;
  align-items: flex-start;
}
.track.signed .half.below {
  flex: 1 1 0;
}
.bar {
  display: block;
  width: 100%;
  min-height: 1px;
}
.bar-positive {
  background: #0d9488;
  border-radius: 2px 2px 0 0;
}
.bar-negative {
  background: #b91c1c;
  border-radius: 0 0 2px 2px;
}
.notice {
  background: #fffbeb;
  border: 1px solid #fde68a;
  color: #92400e;
  border-radius: 8px;
  padding: 8px 12px;
  font-size: 13px;
  margin: 0 0 12px;
}
.table-wrap {
  overflow-x: auto;
}
</style>
