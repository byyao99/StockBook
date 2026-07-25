<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { positionApi } from '../api/client'
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
import type { PortfolioSummary, Position } from '../types'

const PAGE_SIZE = 20

const positions = ref<Position[]>([])
const summary = ref<PortfolioSummary | null>(null)
const total = ref(0)
const offset = ref(0)
const includeClosed = ref(false)
const loading = ref(false)
const error = ref('')

// How many open holdings the valuation totals leave out. Anything above zero
// means the market-value figures cover only part of the book, which the UI says
// out loud rather than presenting a partial total as the whole.
const missingQuotes = computed(() => (summary.value ? unpricedCount(summary.value) : 0))

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
    summary.value = totals
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

    <section v-if="summary" class="cards">
      <div class="stat">
        <span class="stat-label">Cost basis</span>
        <strong class="stat-value">{{ formatCents(summary.total_cost_basis) }}</strong>
        <span class="muted stat-note">{{ summary.open_positions }} open</span>
      </div>
      <div class="stat">
        <span class="stat-label">Market value</span>
        <strong class="stat-value">{{ formatCents(summary.total_market_value) }}</strong>
        <span v-if="missingQuotes > 0" class="badge badge-warn">
          {{ missingQuotes }} unpriced
        </span>
        <span v-else class="muted stat-note">all holdings priced</span>
      </div>
      <div class="stat">
        <span class="stat-label">Unrealized</span>
        <strong class="stat-value" :class="plClass(summary.total_unrealized_pl)">
          {{ formatSignedCents(summary.total_unrealized_pl) }}
        </strong>
        <span class="muted stat-note">
          {{ formatPercentOrUnknown(summaryReturnPct(summary)) }} on priced cost
        </span>
      </div>
      <div class="stat">
        <span class="stat-label">Realized</span>
        <strong class="stat-value" :class="plClass(summary.total_realized_pl)">
          {{ formatSignedCents(summary.total_realized_pl) }}
        </strong>
        <span class="muted stat-note">banked, all time</span>
      </div>
    </section>

    <!-- The valuation totals cover only the priced holdings, so say what they
         leave out instead of letting a partial number read as complete. -->
    <p v-if="missingQuotes > 0" class="notice">
      {{ missingQuotes }} open {{ missingQuotes === 1 ? 'holding has' : 'holdings have' }}
      no quote, so the market value and unrealized figures above exclude
      {{ missingQuotes === 1 ? 'it' : 'them' }}. An admin can set quotes on the
      Instruments page.
    </p>

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
              <td class="num">{{ formatQty(p.quantity) }}</td>
              <td class="num">{{ formatCentsOrUnknown(averageCost(p)) }}</td>
              <td class="num">{{ formatCents(p.cost_basis) }}</td>
              <td class="num">
                {{ formatCentsOrUnknown(p.last_price) }}
                <span v-if="isUnpriced(p)" class="badge badge-warn">no quote</span>
              </td>
              <td class="num">{{ formatCentsOrUnknown(p.market_value) }}</td>
              <td class="num" :class="plClass(p.unrealized_pl)">
                {{ formatSignedOrUnknown(p.unrealized_pl) }}
              </td>
              <td class="num" :class="plClass(p.unrealized_pl)">
                {{ formatPercentOrUnknown(returnPct(p)) }}
              </td>
              <td class="num" :class="plClass(p.realized_pl)">
                {{ formatSignedCents(p.realized_pl) }}
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
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
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
  margin-bottom: 16px;
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
