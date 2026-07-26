<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { reportApi } from '../api/client'
import {
  UNKNOWN,
  formatCents,
  formatPercentOrUnknown,
  formatQty,
  formatSignedCents,
} from '../money'
import type { RealizedSummary } from '../types'

// How many years back the quick picker offers. A ledger older than this is
// still reachable through the date fields; the buttons are for the years
// somebody actually reaches for.
const YEARS_OFFERED = 5

const summaries = ref<RealizedSummary[]>([])
const loading = ref(false)
const error = ref('')

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
    summaries.value = await reportApi.realized(from.value || undefined, to.value || undefined)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
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

// The year in progress is the one a user opens this page to look at.
onMounted(() => selectYear(thisYear))
</script>

<template>
  <div>
    <p v-if="error" class="error">{{ error }}</p>

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
    <p v-else-if="summaries.length === 0" class="muted">
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
