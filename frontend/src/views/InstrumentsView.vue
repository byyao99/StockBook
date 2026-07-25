<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { instrumentApi } from '../api/client'
import { formatCentsOrUnknown, fromCents, toCents } from '../money'
import { isAdmin } from '../session'
import PaginationBar from '../components/PaginationBar.vue'
import { CURRENCIES, MARKETS } from '../types'
import type { Currency, Instrument, RefreshResult } from '../types'

const PAGE_SIZE = 20

const items = ref<Instrument[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(false)
const error = ref('')
const success = ref('')

const searchQuery = ref('')
const marketFilter = ref('')

const editingId = ref<string | null>(null)
const form = reactive<{ symbol: string; name: string; market: string; currency: Currency }>({
  symbol: '',
  name: '',
  market: 'TWSE',
  currency: 'TWD',
})

// The outcome of the last quote refresh, kept so the per-symbol failures stay
// on screen — a count alone would not say which ticker is wrong or why.
const refreshResults = ref<RefreshResult[]>([])
const refreshing = ref(false)

// Picking a market pre-selects the currency it normally trades in, mirroring
// the server's own default. Still overridable for the odd ADR.
function marketChanged() {
  form.currency = form.market === 'NYSE' || form.market === 'NASDAQ' ? 'USD' : 'TWD'
}

// Quotes are edited inline per row rather than through the main form: setting a
// price is a separate endpoint and a different kind of task from editing the
// master data.
const priceDrafts = reactive<Record<string, number | ''>>({})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await instrumentApi.list(PAGE_SIZE, offset.value, {
      q: searchQuery.value.trim() || undefined,
      market: marketFilter.value || undefined,
    })
    items.value = page.items
    total.value = page.pagination.total
    for (const i of page.items) {
      priceDrafts[i.id] = i.last_price === null ? '' : fromCents(i.last_price)
    }
    if (items.value.length === 0 && offset.value > 0) {
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

// Changing a filter restarts paging from the first page.
function applyFilter() {
  offset.value = 0
  load()
}

function resetForm() {
  editingId.value = null
  Object.assign(form, { symbol: '', name: '', market: 'TWSE', currency: 'TWD' })
}

function startEdit(item: Instrument) {
  editingId.value = item.id
  Object.assign(form, {
    symbol: item.symbol,
    name: item.name,
    market: item.market,
    currency: item.currency,
  })
}

async function submit() {
  error.value = ''
  success.value = ''
  const input = {
    symbol: form.symbol,
    name: form.name,
    market: form.market,
    currency: form.currency,
  }
  try {
    if (editingId.value) {
      await instrumentApi.update(editingId.value, input)
      // Renaming does not rewrite the ledger — past trades keep the symbol they
      // were entered with — so say so rather than leaving it a surprise.
      success.value = 'Instrument updated. Existing trades keep their original symbol.'
    } else {
      await instrumentApi.create(input)
      success.value = 'Instrument added.'
    }
    resetForm()
    await load()
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function savePrice(item: Instrument) {
  error.value = ''
  success.value = ''
  const draft = priceDrafts[item.id]
  // An empty box clears the quote rather than setting it to zero: "unknown" and
  // "worth nothing" are different claims.
  const cents = draft === '' ? null : toCents(Number(draft))
  try {
    await instrumentApi.setPrice(item.id, cents)
    success.value =
      cents === null ? `Cleared the quote for ${item.symbol}.` : `Updated ${item.symbol}.`
    await load()
  } catch (e) {
    error.value = (e as Error).message
  }
}

// Fetches quotes for every instrument the provider can address. A partial
// failure is normal — one delisted ticker must not stop the rest — so the
// results are shown per symbol rather than collapsed into a single verdict.
async function refreshQuotes() {
  refreshing.value = true
  error.value = ''
  success.value = ''
  refreshResults.value = []
  try {
    const report = await instrumentApi.refreshQuotes()
    refreshResults.value = report.results.filter((r) => r.status !== 'updated')
    success.value = `Updated ${report.updated} ${report.updated === 1 ? 'quote' : 'quotes'}.`
    if (report.failed > 0) {
      success.value += ` ${report.failed} could not be fetched — see below.`
    }
    await load()
  } catch (e) {
    // A whole-call failure (not configured, no network, not an admin).
    error.value = (e as Error).message
  } finally {
    refreshing.value = false
  }
}

async function remove(item: Instrument) {
  if (!confirm(`Delete ${item.symbol}?`)) return
  error.value = ''
  success.value = ''
  try {
    await instrumentApi.remove(item.id)
    success.value = 'Instrument deleted.'
    await load()
  } catch (e) {
    // The server refuses to delete an instrument that trades reference; its
    // message explains why.
    error.value = (e as Error).message
  }
}

/** Renders a quote's age, or a nudge when there is none. */
function quoteAge(item: Instrument): string {
  if (!item.price_updated_at) return 'never set'
  const days = Math.floor((Date.now() - Date.parse(item.price_updated_at)) / 86_400_000)
  if (days <= 0) return 'today'
  if (days === 1) return 'yesterday'
  return `${days} days ago`
}

onMounted(load)
</script>

<template>
  <div>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="success" class="success">{{ success }}</p>

    <section v-if="isAdmin" class="card">
      <h2 class="section-title">{{ editingId ? 'Edit Instrument' : 'New Instrument' }}</h2>
      <form @submit.prevent="submit">
        <div class="grid">
          <div class="field">
            <label>Symbol</label>
            <input v-model="form.symbol" required maxlength="20" placeholder="e.g. 2330" />
          </div>
          <div class="field">
            <label>Name</label>
            <input v-model="form.name" required maxlength="120" placeholder="e.g. TSMC" />
          </div>
          <div class="field">
            <label>Market</label>
            <select v-model="form.market" required @change="marketChanged">
              <option v-for="m in MARKETS" :key="m" :value="m">{{ m }}</option>
            </select>
          </div>
          <div class="field">
            <label>Currency</label>
            <!-- Locked once trades exist: changing it would reinterpret every
                 cost basis already recorded against this instrument, and the
                 server refuses the change anyway. -->
            <select v-model="form.currency" required>
              <option v-for="c in CURRENCIES" :key="c" :value="c">{{ c }}</option>
            </select>
          </div>
        </div>
        <div class="actions">
          <button type="submit" class="btn-primary">
            {{ editingId ? 'Save Changes' : 'Add Instrument' }}
          </button>
          <button v-if="editingId" type="button" class="btn-secondary" @click="resetForm">
            Cancel
          </button>
        </div>
      </form>
    </section>

    <section class="card">
      <div class="head">
        <h2 class="section-title">Instruments ({{ total }})</h2>
        <div class="filter">
          <button v-if="isAdmin" class="btn-primary" :disabled="refreshing" @click="refreshQuotes">
            {{ refreshing ? 'Fetching…' : 'Refresh quotes' }}
          </button>
          <input
            v-model="searchQuery"
            type="search"
            placeholder="Search symbol or name…"
            @change="applyFilter"
            @keyup.enter="applyFilter"
          />
          <select v-model="marketFilter" @change="applyFilter">
            <option value="">All markets</option>
            <option v-for="m in MARKETS" :key="m" :value="m">{{ m }}</option>
          </select>
        </div>
      </div>

      <ul v-if="refreshResults.length > 0" class="refresh-log">
        <li v-for="r in refreshResults" :key="r.instrument_id">
          <span :class="['badge', r.status === 'failed' ? 'badge-sell' : 'badge-warn']">
            {{ r.status }}
          </span>
          <strong>{{ r.symbol }}</strong>
          <span class="muted">{{ r.error }}</span>
        </li>
      </ul>

      <p v-if="loading" class="muted">Loading…</p>
      <p v-else-if="items.length === 0" class="muted">No instruments found.</p>
      <div v-else class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Symbol</th>
              <th>Market</th>
              <th>Ccy</th>
              <th class="num">Last price</th>
              <th>Quote set</th>
              <th v-if="isAdmin"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in items" :key="item.id">
              <td>
                <strong>{{ item.symbol }}</strong>
                <div class="muted">{{ item.name }}</div>
              </td>
              <td>{{ item.market }}</td>
              <td class="muted">{{ item.currency }}</td>
              <td class="num">
                <template v-if="isAdmin">
                  <input
                    v-model="priceDrafts[item.id]"
                    class="price-input"
                    type="number"
                    min="0"
                    step="0.01"
                    placeholder="—"
                    @keyup.enter="savePrice(item)"
                  />
                </template>
                <template v-else>{{
                  formatCentsOrUnknown(item.last_price, item.currency)
                }}</template>
              </td>
              <td>
                <span :class="item.price_updated_at ? 'muted' : 'badge badge-warn'">
                  {{ quoteAge(item) }}
                </span>
              </td>
              <td v-if="isAdmin" class="row-actions">
                <button class="btn-secondary" @click="savePrice(item)">Save price</button>
                <button class="btn-secondary" @click="startEdit(item)">Edit</button>
                <button class="btn-danger" @click="remove(item)">Delete</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="isAdmin" class="muted hint">
        Clear a price box and save to mark an instrument as unquoted; its holdings
        then show an unknown market value rather than zero.
      </p>

      <PaginationBar :limit="PAGE_SIZE" :offset="offset" :total="total" @change="changePage" />
    </section>
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
}
.head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  gap: 12px;
}
.filter {
  display: flex;
  gap: 8px;
}
.filter input,
.filter select {
  width: auto;
}
.actions {
  display: flex;
  gap: 8px;
}
.row-actions {
  display: flex;
  gap: 6px;
  justify-content: flex-end;
}
.price-input {
  width: 110px;
  text-align: right;
}
.hint {
  font-size: 12px;
  margin-top: 12px;
}
.refresh-log {
  list-style: none;
  margin: 0 0 12px;
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
}
.table-wrap {
  overflow-x: auto;
}
</style>
