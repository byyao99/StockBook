<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { instrumentApi } from '../api/client'
import { formatCentsOrUnknown } from '../money'
import { isAdmin } from '../session'
import PaginationBar from '../components/PaginationBar.vue'
import { MARKETS } from '../types'
import type { Instrument, InstrumentCandidate, RefreshResult } from '../types'

const PAGE_SIZE = 20

const items = ref<Instrument[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(false)
const error = ref('')
const success = ref('')

const searchQuery = ref('')
const marketFilter = ref('')

// Looking an instrument up with the provider and adding it in one click. This
// is the only way to add one: an instrument the provider cannot price has no
// use here, since every number on the holdings page derives from a quote.
// Adding a candidate therefore also arrives already priced.
const lookupQuery = ref('')
const candidates = ref<InstrumentCandidate[]>([])
const searching = ref(false)
const searched = ref(false)

// Renaming is the one thing about an instrument this app decides rather than
// the provider — useful for putting a Chinese name on a listing the provider
// only knows in capitalised English.
const renamingId = ref<string | null>(null)
const renameDraft = ref('')

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

async function lookup() {
  const q = lookupQuery.value.trim()
  if (!q) return
  searching.value = true
  searched.value = false
  error.value = ''
  success.value = ''
  candidates.value = []
  try {
    candidates.value = await instrumentApi.search(q)
    searched.value = true
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    searching.value = false
  }
}

async function addCandidate(candidate: InstrumentCandidate) {
  error.value = ''
  success.value = ''
  try {
    const created = await instrumentApi.create({
      symbol: candidate.symbol,
      market: candidate.market,
    })
    // The server prices it on the way in, so say so — it saves the user
    // wondering whether a quote still has to be fetched separately.
    success.value = `Added ${created.symbol} — ${created.name}, priced at ${formatCentsOrUnknown(
      created.last_price,
      created.currency,
    )}.`
    // Mark it in place rather than re-searching, so the rest of the results
    // stay put and several can be added in a row.
    candidate.exists = true
    await load()
  } catch (e) {
    error.value = (e as Error).message
  }
}

function clearLookup() {
  lookupQuery.value = ''
  candidates.value = []
  searched.value = false
}

function startRename(item: Instrument) {
  renamingId.value = item.id
  renameDraft.value = item.name
}

function cancelRename() {
  renamingId.value = null
  renameDraft.value = ''
}

async function saveRename(item: Instrument) {
  const name = renameDraft.value.trim()
  if (!name) return
  error.value = ''
  success.value = ''
  try {
    await instrumentApi.rename(item.id, name)
    success.value = `Renamed ${item.symbol}.`
    cancelRename()
    await load()
  } catch (e) {
    error.value = (e as Error).message
  }
}

// The outcome of the last quote refresh, kept so the per-symbol failures stay
// on screen — a count alone would not say which ticker is wrong or why.
const refreshResults = ref<RefreshResult[]>([])
const refreshing = ref(false)

// Fetches quotes for every instrument. A partial failure is normal — one
// delisted ticker must not stop the rest — so the results are shown per symbol
// rather than collapsed into a single verdict.
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
      // Say why nothing happened, or a no-op refresh looks like a broken button.
      success.value += ` ${report.fresh} already current.`
    }
    if (report.failed > 0) {
      success.value += ` ${report.failed} could not be fetched — see below.`
    }
    await load()
  } catch (e) {
    // A whole-call failure (not configured, no network, rate-limited).
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

/** Renders a quote's age. Every instrument has one — none can be added unpriced. */
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
      <h2 class="section-title">Add an Instrument</h2>
      <p class="muted lookup-hint">
        Search by stock number or company name and add it in one click — the
        symbol, market, currency and current price all come from the price
        provider, so there is nothing to type and nothing to mistype. Chinese
        names are not matched; use the stock number or the English name.
      </p>
      <form class="lookup" @submit.prevent="lookup">
        <input
          v-model="lookupQuery"
          type="search"
          placeholder="e.g. 2330, 6488, TSLA, taiwan semiconductor"
        />
        <button type="submit" class="btn-primary" :disabled="searching">
          {{ searching ? 'Searching…' : 'Search' }}
        </button>
        <button
          v-if="candidates.length > 0"
          type="button"
          class="btn-secondary"
          @click="clearLookup"
        >
          Clear
        </button>
      </form>

      <p v-if="searched && candidates.length === 0" class="muted">
        Nothing found that this app can track. Only TWSE, TPEX, NYSE and NASDAQ
        listings can be priced, so other exchanges, indices and futures are left
        out — an instrument with no quote could not be valued anyway.
      </p>
      <ul v-if="candidates.length > 0" class="candidates">
        <li v-for="c in candidates" :key="c.ticker">
          <div class="candidate-id">
            <strong>{{ c.symbol }}</strong>
            <span class="ticker">{{ c.ticker }}</span>
          </div>
          <div class="candidate-name">{{ c.name }}</div>
          <div class="muted candidate-meta">{{ c.market }} · {{ c.currency }}</div>
          <button v-if="c.exists" class="btn-secondary" disabled>Already added</button>
          <button v-else class="btn-primary" @click="addCandidate(c)">Add</button>
        </li>
      </ul>
    </section>

    <section class="card">
      <div class="head">
        <h2 class="section-title">Instruments ({{ total }})</h2>
        <div class="filter">
          <button class="btn-primary" :disabled="refreshing" @click="refreshQuotes">
            {{ refreshing ? 'Fetching…' : 'Refresh quotes' }}
          </button>
          <input
            v-model="searchQuery"
            type="search"
            placeholder="Filter the list…"
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
          <span v-if="r.ticker" class="ticker">looked up as {{ r.ticker }}</span>
          <span class="muted">{{ r.error }}</span>
        </li>
      </ul>

      <p v-if="loading" class="muted">Loading…</p>
      <p v-else-if="items.length === 0" class="muted">
        No instruments yet.
        <template v-if="isAdmin">Search for one above to get started.</template>
      </p>
      <div v-else class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Symbol</th>
              <th>Market</th>
              <th>Ccy</th>
              <th class="num">Last price</th>
              <th>Quote from</th>
              <th v-if="isAdmin"></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in items" :key="item.id">
              <td>
                <strong>{{ item.symbol }}</strong>
                <div v-if="renamingId === item.id" class="rename">
                  <input v-model="renameDraft" maxlength="120" @keyup.enter="saveRename(item)" />
                  <button class="btn-primary" @click="saveRename(item)">Save</button>
                  <button class="btn-secondary" @click="cancelRename">Cancel</button>
                </div>
                <div v-else class="muted">{{ item.name }}</div>
              </td>
              <td>{{ item.market }}</td>
              <td class="muted">{{ item.currency }}</td>
              <td class="num">{{ formatCentsOrUnknown(item.last_price, item.currency) }}</td>
              <td class="muted">{{ quoteAge(item) }}</td>
              <td v-if="isAdmin" class="row-actions">
                <button class="btn-secondary" @click="startRename(item)">Rename</button>
                <button class="btn-danger" @click="remove(item)">Delete</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="isAdmin" class="muted hint">
        Prices come from the provider; refreshing leaves quotes newer than 15
        minutes alone. Only the display name is editable — a listing added
        wrongly should be deleted and re-added, which is refused once trades
        reference it.
      </p>

      <PaginationBar :limit="PAGE_SIZE" :offset="offset" :total="total" @change="changePage" />
    </section>
  </div>
</template>

<style scoped>
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
.filter select,
.filter button {
  width: auto;
}
.row-actions {
  display: flex;
  gap: 6px;
  justify-content: flex-end;
}
.rename {
  display: flex;
  gap: 6px;
  margin-top: 4px;
}
.rename input {
  width: 180px;
}
.rename button {
  width: auto;
}
.hint {
  font-size: 12px;
  margin-top: 12px;
}
.lookup {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}
.lookup input {
  flex: 1;
}
.lookup button {
  width: auto;
  white-space: nowrap;
}
.lookup-hint {
  font-size: 13px;
  margin: 0 0 12px;
}
.candidates {
  list-style: none;
  margin: 0;
  padding: 0;
}
.candidates li {
  display: grid;
  grid-template-columns: minmax(140px, auto) 1fr auto auto;
  align-items: center;
  gap: 12px;
  padding: 8px 0;
  border-top: 1px solid #f0f0f0;
}
.candidates button {
  width: auto;
}
.candidate-id {
  display: flex;
  align-items: center;
  gap: 8px;
}
.candidate-name {
  font-size: 14px;
}
.candidate-meta {
  font-size: 12px;
  white-space: nowrap;
}
@media (max-width: 640px) {
  .candidates li {
    grid-template-columns: 1fr auto;
  }
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
.table-wrap {
  overflow-x: auto;
}
</style>
