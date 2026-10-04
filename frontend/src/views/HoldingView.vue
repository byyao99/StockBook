<script setup lang="ts">
/**
 * One holding's own page.
 *
 * The list on /positions answers "what do I own?"; this answers "how has this
 * one done?", which nothing here could ask before. Everything on it is
 * **lifetime** rather than period-bounded — unlike the reports, which are read
 * a year at a time. A question about one position is not improved by hiding
 * half its history: the average cost every figure here is built on was formed
 * by trades a window would have excluded.
 *
 * The chart is the book's own equity curve narrowed to this instrument, drawn
 * by the same component with the same arithmetic. It narrows the *ledger* and
 * nothing else — contributions are still divided out of the index, and the
 * benchmark still runs the same money through the chosen index, which at this
 * scale asks the sharper question of whether this one holding was worth owning
 * instead of it.
 */
import { computed, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { positionApi, reportApi, transactionApi } from '../api/client'
import {
  formatBpsMagnitudeOrUnknown,
  formatBpsOrUnknown,
  formatCents,
  formatCentsOrUnknown,
  formatPercentOrUnknown,
  formatSignedCents,
  formatSignedOrUnknown,
} from '../money'
import { formatShares } from '../shares'
import { averageCost, returnPct } from '../positionMath'
import EquityCurveChart from '../components/EquityCurveChart.vue'
import PaginationBar from '../components/PaginationBar.vue'
import SplitWarnings from '../components/SplitWarnings.vue'
import type { CurrencyCurve, HoldingDetail, Transaction, UnadjustedSplit } from '../types'

const PAGE_SIZE = 20

const route = useRoute()
const instrumentId = computed(() => String(route.params.id ?? ''))

const detail = ref<HoldingDetail | null>(null)
const curve = ref<CurrencyCurve | null>(null)
const entries = ref<Transaction[]>([])
const splits = ref<UnadjustedSplit[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(false)
const error = ref('')

const position = computed(() => detail.value?.position ?? null)
const currency = computed(() => position.value?.currency)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [held, page, curves, allSplits] = await Promise.all([
      positionApi.detail(instrumentId.value),
      transactionApi.list(PAGE_SIZE, offset.value, { instrumentId: instrumentId.value }),
      reportApi.curve(undefined, undefined, instrumentId.value),
      // Acknowledged ones are kept here, dimmed: this is where the undo lives,
      // and a dismissal the reader cannot find again is not reversible.
      reportApi.splits(true),
    ])
    detail.value = held
    entries.value = page.items
    total.value = page.pagination.total
    // One instrument is held in exactly one currency, so the narrowed curve
    // comes back as a single block or as none at all.
    curve.value = curves[0] ?? null
    splits.value = allSplits.filter((s) => s.instrument_id === instrumentId.value)
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

function plClass(value: number | null): string {
  if (value === null) return 'muted'
  return value < 0 ? 'loss' : 'gain'
}

function sideBadge(side: string): string {
  return side === 'buy' ? 'badge-buy' : side === 'sell' ? 'badge-sell' : 'badge-dividend'
}

/**
 * What cash this holding has actually returned so far: proceeds and payouts
 * against what went in. It is a different question from realized profit, which
 * measures the sales against the average cost they released — the two differ by
 * whatever is still held, and a reader looking at a half-sold position is
 * usually asking this one.
 */
const netCash = computed(() => {
  const d = detail.value
  return d === null ? 0 : d.proceeds + d.dividends - d.invested
})

/** Dates are sliced off the UTC timestamp, the same way the ledger shows them. */
function dateOf(timestamp: string | null): string {
  return timestamp === null ? '—' : timestamp.slice(0, 10)
}

onMounted(load)
// The symbol in the ledger below links to other holdings, so the id can change
// without the component being torn down.
watch(instrumentId, () => {
  offset.value = 0
  load()
})
</script>

<template>
  <div>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="loading && detail === null" class="muted">Loading…</p>

    <template v-if="detail && position">
      <div class="head">
        <div>
          <h2 class="holding-title">
            {{ position.symbol }}
            <span class="muted">{{ position.name }}</span>
          </h2>
          <p class="muted holding-meta">
            {{ position.market }} · {{ position.currency }} ·
            {{ detail.buys }} {{ detail.buys === 1 ? 'buy' : 'buys' }}, {{ detail.sells }}
            {{ detail.sells === 1 ? 'sale' : 'sales' }}, {{ detail.dividend_count }}
            {{ detail.dividend_count === 1 ? 'payout' : 'payouts' }}
            <template v-if="detail.first_traded_at">
              · held since {{ dateOf(detail.first_traded_at) }}
            </template>
          </p>
        </div>
        <div class="head-links">
          <RouterLink to="/positions" class="nav-back">← Holdings</RouterLink>
          <!-- Only an open holding links through to the research feed, which
               drops closed ones deliberately: a link from a sold position would
               land on a page with nothing to say about it. -->
          <RouterLink
            v-if="position.quantity > 0"
            :to="{ name: 'research', query: { instrument_id: position.instrument_id } }"
            class="nav-back"
          >
            Research →
          </RouterLink>
        </div>
      </div>

      <SplitWarnings :splits="splits" @changed="load" />

      <div class="cards">
        <div class="stat">
          <span class="stat-label">Shares</span>
          <strong class="stat-value">{{ formatShares(position.quantity) }}</strong>
          <span class="muted stat-note">
            at {{ formatCentsOrUnknown(averageCost(position), currency) }} average cost
          </span>
        </div>
        <div class="stat">
          <span class="stat-label">Market value</span>
          <strong class="stat-value">
            {{ formatCentsOrUnknown(position.market_value, currency) }}
          </strong>
          <span class="muted stat-note">
            last {{ formatCentsOrUnknown(position.last_price, currency) }}
            <template v-if="position.price_updated_at">
              · {{ dateOf(position.price_updated_at) }}
            </template>
          </span>
        </div>
        <div class="stat">
          <span class="stat-label">Unrealized</span>
          <strong class="stat-value" :class="plClass(position.unrealized_pl)">
            {{ formatSignedOrUnknown(position.unrealized_pl, currency) }}
          </strong>
          <span class="muted stat-note">
            {{ formatPercentOrUnknown(returnPct(position)) }} on
            {{ formatCents(position.cost_basis, currency) }} cost
          </span>
        </div>
        <!-- Split apart because the two are taxed differently in most places,
             and a holding that lost on price while making it back on income
             must not read as a flat one. -->
        <div class="stat">
          <span class="stat-label">Realized</span>
          <strong class="stat-value" :class="plClass(detail.realized_pl)">
            {{ formatSignedCents(detail.realized_pl, currency) }}
          </strong>
          <span class="muted stat-note">
            {{ formatSignedCents(detail.trading_pl, currency) }} trading ·
            {{ formatCents(detail.dividends, currency) }} dividends
          </span>
        </div>
        <div class="stat">
          <span class="stat-label">Annualized (XIRR)</span>
          <strong class="stat-value" :class="plClass(detail.xirr_bps)">
            {{ formatBpsOrUnknown(detail.xirr_bps) }}
          </strong>
          <span class="muted stat-note">
            {{
              detail.xirr_bps === null
                ? (detail.unavailable ?? 'not enough history to measure')
                : 'per year on this holding alone'
            }}
          </span>
        </div>
        <!-- Against cost rather than market value, because that is what a
             long-term holder is asking: what the money they committed now pays
             them. The market-value yield is on every quote page already, and it
             says something about the price rather than about this book. -->
        <div class="stat">
          <span class="stat-label">Yield on cost</span>
          <!-- A yield has a size but no direction, exactly as a drawdown and a
               fee rate do, so it goes through the unsigned formatter: the
               signed one would print an income holding's 4% as "+4.00%" and
               invite it to be read as a gain. -->
          <strong class="stat-value">
            {{ formatBpsMagnitudeOrUnknown(detail.yield_on_cost_bps) }}
          </strong>
          <span class="muted stat-note">
            {{ formatCents(detail.trailing_dividends, currency) }} banked over the last 12 months
          </span>
        </div>
      </div>

      <p v-if="detail.unstamped_entries > 0" class="notice">
        {{ detail.unstamped_entries }}
        {{ detail.unstamped_entries === 1 ? 'entry has' : 'entries have' }} no recorded result, so
        the trading and dividend split above does not account for
        {{ detail.unstamped_entries === 1 ? 'it' : 'them' }}. The realized total beside it is the
        position's own running figure and is complete.
      </p>

      <section class="card">
        <div class="head">
          <h2 class="section-title">This holding's history</h2>
          <span class="muted">
            {{ formatSignedCents(netCash, currency) }} net cash so far ·
            {{ formatCents(detail.invested, currency) }} in,
            {{ formatCents(detail.proceeds + detail.dividends, currency) }} out
          </span>
        </div>
        <p v-if="curve === null || curve.points.length === 0" class="muted">
          {{
            curve?.unavailable ??
            'No stored price history covers when this was held — press Sync prices on Reports.'
          }}
        </p>
        <EquityCurveChart v-else :curve="curve" :currency="curve.currency" />
      </section>

      <section class="card">
        <h2 class="section-title">Entries ({{ total }})</h2>
        <p v-if="entries.length === 0" class="muted">No entries.</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Date</th>
                <th>Side</th>
                <th class="num">Shares</th>
                <th class="num">Price</th>
                <th class="num">Fee</th>
                <th class="num">Net</th>
                <th class="num">Realized</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="t in entries" :key="t.id">
                <td>{{ t.traded_at.slice(0, 10) }}</td>
                <td>
                  <span :class="['badge', sideBadge(t.side)]">{{ t.side }}</span>
                  <div v-if="t.note" class="muted">{{ t.note }}</div>
                </td>
                <td class="num">{{ formatShares(t.quantity) }}</td>
                <td class="num">{{ formatCents(t.price, currency) }}</td>
                <td class="num">{{ formatCents(t.fee, currency) }}</td>
                <td class="num">{{ formatCents(t.net_amount, currency) }}</td>
                <!-- A buy realizes nothing, which is not the same as banking
                     zero, so it shows an em dash rather than $0.00. -->
                <td class="num" :class="plClass(t.realized_pl)">
                  {{ formatSignedOrUnknown(t.realized_pl, currency) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <PaginationBar :limit="PAGE_SIZE" :offset="offset" :total="total" @change="changePage" />
      </section>
    </template>
  </div>
</template>

<style scoped>
.head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}
.holding-title {
  margin: 0;
  font-size: 22px;
}
.holding-title .muted {
  font-size: 15px;
  font-weight: 400;
  margin-left: 8px;
}
.holding-meta {
  margin: 4px 0 0;
  font-size: 13px;
}
.head-links {
  display: flex;
  gap: 12px;
  flex-shrink: 0;
}
.nav-back {
  color: #475569;
  font-size: 14px;
  text-decoration: none;
}
.nav-back:hover {
  color: #0d9488;
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
.section-title {
  margin: 0;
}
.card .head {
  margin-bottom: 8px;
  align-items: center;
}
/* Wide tables scroll inside their own box rather than the page. */
.table-wrap {
  overflow-x: auto;
}
</style>
