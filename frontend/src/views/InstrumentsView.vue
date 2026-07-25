<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { instrumentApi } from '../api/client'
import { formatCentsOrUnknown, fromCents, toCents } from '../money'
import { isAdmin } from '../session'
import PaginationBar from '../components/PaginationBar.vue'
import { MARKETS } from '../types'
import type { Instrument } from '../types'

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
const form = reactive<{ symbol: string; name: string; market: string }>({
  symbol: '',
  name: '',
  market: 'TWSE',
})

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
  Object.assign(form, { symbol: '', name: '', market: 'TWSE' })
}

function startEdit(item: Instrument) {
  editingId.value = item.id
  Object.assign(form, { symbol: item.symbol, name: item.name, market: item.market })
}

async function submit() {
  error.value = ''
  success.value = ''
  const input = { symbol: form.symbol, name: form.name, market: form.market }
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
            <select v-model="form.market" required>
              <option v-for="m in MARKETS" :key="m" :value="m">{{ m }}</option>
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

      <p v-if="loading" class="muted">Loading…</p>
      <p v-else-if="items.length === 0" class="muted">No instruments found.</p>
      <div v-else class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Symbol</th>
              <th>Market</th>
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
                <template v-else>{{ formatCentsOrUnknown(item.last_price) }}</template>
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
.table-wrap {
  overflow-x: auto;
}
</style>
