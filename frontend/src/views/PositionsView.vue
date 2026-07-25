<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { instrumentApi, positionApi } from '../api/client'
import {
  formatCents,
  formatCentsOrUnknown,
  formatPercentOrUnknown,
  formatQty,
  formatSignedCents,
  formatSignedOrUnknown,
} from '../money'
import {
  averageCost,
  isUnpriced,
  returnPct,
  summaryReturnPct,
  unpricedCount,
} from '../positionMath'
import PaginationBar from '../components/PaginationBar.vue'
import type { CurrencySummary, Position, RefreshResult } from '../types'

const PAGE_SIZE = 20

const positions = ref<Position[]>([])
// One summary per currency held. They are never combined into a grand total:
// there is no FX rate in this system, so adding TWD to USD would produce a
// number that means nothing.
const summaries = ref<CurrencySummary[]>([])
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

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [page, totals] = await Promise.all([
      positionApi.list(PAGE_SIZE, offset.value, includeClosed.value),
      positionApi.summary(),
    ])
    positions.value = page.items
    total.value = page.pagination.total
    summaries.value = totals
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
async function refreshQuotes() {
  refreshing.value = true
  error.value = ''
  success.value = ''
  refreshResults.value = []
  try {
    const report = await instrumentApi.refreshQuotes()
    refreshResults.value = report.results.filter((r) => r.status !== 'updated')
    success.value = `Updated ${report.updated} ${report.updated === 1 ? 'quote' : 'quotes'}.`
    if (report.fresh > 0) {
      // Without this a no-op refresh looks like a button that does nothing.
      success.value += ` ${report.fresh} already current.`
    }
    if (report.failed > 0) {
      success.value += ` ${report.failed} could not be fetched — see below.`
    }
    await load()
  } catch (e) {
    error.value = (e as Error).message
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
function plClass(value: number | null): string {
  if (value === null) return 'muted'
  return value < 0 ? 'loss' : 'gain'
}

onMounted(load)
</script>

<template>
  <div>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="success" class="success">{{ success }}</p>

    <div class="page-actions">
      <button class="btn-secondary" :disabled="refreshing" @click="refreshQuotes">
        {{ refreshing ? 'Fetching quotes…' : 'Refresh quotes' }}
      </button>
    </div>

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
          <span class="muted stat-note">banked, all time</span>
        </div>
      </div>
      <p v-if="unpricedCount(s) > 0" class="notice">
        {{ unpricedCount(s) }} open {{ unpricedCount(s) === 1 ? 'holding has' : 'holdings have' }}
        no quote, so the {{ s.currency }} market value and unrealized figures exclude
        {{ unpricedCount(s) === 1 ? 'it' : 'them' }}. Try Refresh quotes above.
      </p>
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
              <th class="num">Unrealized</th>
              <th class="num">Return</th>
              <th class="num">Realized</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in positions" :key="p.id" :class="{ closed: p.quantity === 0 }">
              <td>
                <strong>{{ p.symbol }}</strong>
                <div class="muted">{{ p.name }}</div>
              </td>
              <td class="muted">{{ p.currency }}</td>
              <td class="num">{{ formatQty(p.quantity) }}</td>
              <td class="num">{{ formatCentsOrUnknown(averageCost(p), p.currency) }}</td>
              <td class="num">{{ formatCents(p.cost_basis, p.currency) }}</td>
              <td class="num">
                {{ formatCentsOrUnknown(p.last_price, p.currency) }}
                <span v-if="isUnpriced(p)" class="badge badge-warn">no quote</span>
              </td>
              <td class="num">{{ formatCentsOrUnknown(p.market_value, p.currency) }}</td>
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
