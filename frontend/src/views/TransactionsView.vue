<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { instrumentApi, transactionApi } from '../api/client'
import { formatCents, formatQty, fromCents, toCents } from '../money'
import PaginationBar from '../components/PaginationBar.vue'
import { SIDES } from '../types'
import type { Instrument, Transaction, TransactionSide } from '../types'

const PAGE_SIZE = 20

const entries = ref<Transaction[]>([])
const instruments = ref<Instrument[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(false)
const error = ref('')
const success = ref('')

const instrumentFilter = ref('')
const sideFilter = ref<TransactionSide | ''>('')

const editingId = ref<string | null>(null)

// The form edits dollars; the API speaks cents. The conversion happens here at
// the UI edge and nowhere else.
const form = reactive<{
  instrumentId: string
  side: TransactionSide
  quantity: number
  priceDollars: number
  feeDollars: number
  tradedAt: string
  note: string
}>({
  instrumentId: '',
  side: 'buy',
  quantity: 0,
  priceDollars: 0,
  feeDollars: 0,
  tradedAt: today(),
  note: '',
})

function today(): string {
  return new Date().toISOString().slice(0, 10)
}

// A preview of the cash movement, computed the same way the server will. It is
// shown for the user's benefit only — the stored figure is always the server's.
const netPreview = computed(() => {
  const gross = form.quantity * toCents(form.priceDollars)
  const fee = toCents(form.feeDollars)
  return form.side === 'sell' ? gross - fee : gross + fee
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await transactionApi.list(PAGE_SIZE, offset.value, {
      instrumentId: instrumentFilter.value || undefined,
      side: sideFilter.value || undefined,
    })
    entries.value = page.items
    total.value = page.pagination.total
    if (entries.value.length === 0 && offset.value > 0) {
      offset.value = Math.max(0, offset.value - PAGE_SIZE)
      await load()
    }
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function loadInstruments() {
  try {
    // One large page: the dropdown needs every instrument, not a page of them.
    const page = await instrumentApi.list(100, 0)
    instruments.value = page.items
    if (!form.instrumentId && page.items.length > 0) {
      form.instrumentId = page.items[0].id
    }
  } catch (e) {
    error.value = (e as Error).message
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
  Object.assign(form, {
    instrumentId: instruments.value[0]?.id ?? '',
    side: 'buy',
    quantity: 0,
    priceDollars: 0,
    feeDollars: 0,
    tradedAt: today(),
    note: '',
  })
}

function startEdit(t: Transaction) {
  editingId.value = t.id
  Object.assign(form, {
    instrumentId: t.instrument_id,
    side: t.side,
    quantity: t.quantity,
    priceDollars: fromCents(t.price),
    feeDollars: fromCents(t.fee),
    tradedAt: t.traded_at.slice(0, 10),
    note: t.note,
  })
}

async function submit() {
  error.value = ''
  success.value = ''
  // Send a bare date as midday UTC so a timezone shift cannot push the trade
  // into tomorrow, which the server rejects as a future trade.
  const tradedAt = new Date(`${form.tradedAt}T12:00:00Z`).toISOString()
  try {
    if (editingId.value) {
      await transactionApi.update(editingId.value, {
        quantity: form.quantity,
        price: toCents(form.priceDollars),
        fee: toCents(form.feeDollars),
        traded_at: tradedAt,
        note: form.note,
      })
      success.value = 'Trade updated and holdings rebuilt.'
    } else {
      await transactionApi.create({
        instrument_id: form.instrumentId,
        side: form.side,
        quantity: form.quantity,
        price: toCents(form.priceDollars),
        fee: toCents(form.feeDollars),
        traded_at: tradedAt,
        note: form.note,
      })
      success.value = 'Trade recorded.'
    }
    resetForm()
    await load()
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function remove(t: Transaction) {
  if (!confirm(`Delete this ${t.side} of ${formatQty(t.quantity)} ${t.symbol}?`)) return
  error.value = ''
  success.value = ''
  try {
    await transactionApi.remove(t.id)
    success.value = 'Trade deleted and holdings rebuilt.'
    await load()
  } catch (e) {
    // Deleting a buy a later sell drew on is refused by the server; surfacing
    // its message tells the user which entry would have been left short.
    error.value = (e as Error).message
  }
}

onMounted(async () => {
  await loadInstruments()
  await load()
})
</script>

<template>
  <div>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="success" class="success">{{ success }}</p>

    <section class="card">
      <h2 class="section-title">{{ editingId ? 'Edit Trade' : 'Record a Trade' }}</h2>
      <p v-if="instruments.length === 0" class="muted">
        No instruments exist yet. An admin must add one on the Instruments page first.
      </p>
      <form v-else @submit.prevent="submit">
        <div class="grid">
          <div class="field">
            <label>Instrument</label>
            <!-- The instrument cannot move once recorded: changing it would
                 shift the entry to a different holding, which the API expects
                 as a delete plus a fresh entry. -->
            <select v-model="form.instrumentId" required :disabled="editingId !== null">
              <option v-for="i in instruments" :key="i.id" :value="i.id">
                {{ i.symbol }} — {{ i.name }}
              </option>
            </select>
          </div>
          <div class="field">
            <label>Side</label>
            <select v-model="form.side" required :disabled="editingId !== null">
              <option v-for="s in SIDES" :key="s" :value="s">{{ s }}</option>
            </select>
          </div>
          <div class="field">
            <label>Shares</label>
            <input v-model.number="form.quantity" type="number" min="1" step="1" required />
          </div>
          <div class="field">
            <label>Price ($)</label>
            <input v-model.number="form.priceDollars" type="number" min="0" step="0.01" required />
          </div>
          <div class="field">
            <label>Fees &amp; tax ($)</label>
            <input v-model.number="form.feeDollars" type="number" min="0" step="0.01" />
          </div>
          <div class="field">
            <label>Trade date</label>
            <input v-model="form.tradedAt" type="date" :max="today()" required />
          </div>
        </div>
        <div class="field">
          <label>Note</label>
          <input v-model="form.note" maxlength="500" placeholder="optional" />
        </div>

        <p class="preview muted">
          {{ form.side === 'sell' ? 'Proceeds' : 'Total cost' }}:
          <strong>{{ formatCents(netPreview) }}</strong>
          <span class="hint">(the server recomputes this; this is a preview)</span>
        </p>

        <div class="actions">
          <button type="submit" class="btn-primary">
            {{ editingId ? 'Save Changes' : 'Record Trade' }}
          </button>
          <button v-if="editingId" type="button" class="btn-secondary" @click="resetForm">
            Cancel
          </button>
        </div>
      </form>
    </section>

    <section class="card">
      <div class="head">
        <h2 class="section-title">Ledger ({{ total }})</h2>
        <div class="filter">
          <select v-model="instrumentFilter" @change="applyFilter">
            <option value="">All instruments</option>
            <option v-for="i in instruments" :key="i.id" :value="i.id">{{ i.symbol }}</option>
          </select>
          <select v-model="sideFilter" @change="applyFilter">
            <option value="">Buys and sells</option>
            <option v-for="s in SIDES" :key="s" :value="s">{{ s }}</option>
          </select>
        </div>
      </div>

      <p v-if="loading" class="muted">Loading…</p>
      <p v-else-if="entries.length === 0" class="muted">No trades recorded yet.</p>
      <div v-else class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Date</th>
              <th>Symbol</th>
              <th>Side</th>
              <th class="num">Shares</th>
              <th class="num">Price</th>
              <th class="num">Fees</th>
              <th class="num">Net</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in entries" :key="t.id">
              <td>{{ t.traded_at.slice(0, 10) }}</td>
              <td>
                <strong>{{ t.symbol }}</strong>
                <div v-if="t.note" class="muted">{{ t.note }}</div>
              </td>
              <td>
                <span :class="['badge', t.side === 'buy' ? 'badge-buy' : 'badge-sell']">
                  {{ t.side }}
                </span>
              </td>
              <td class="num">{{ formatQty(t.quantity) }}</td>
              <td class="num">{{ formatCents(t.price) }}</td>
              <td class="num">{{ formatCents(t.fee) }}</td>
              <td class="num">{{ formatCents(t.net_amount) }}</td>
              <td class="row-actions">
                <button class="btn-secondary" @click="startEdit(t)">Edit</button>
                <button class="btn-danger" @click="remove(t)">Delete</button>
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
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
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
.filter select {
  width: auto;
}
.preview {
  margin: 12px 0;
  font-size: 14px;
}
.preview strong {
  color: #111827;
  font-variant-numeric: tabular-nums;
}
.hint {
  margin-left: 8px;
  font-size: 12px;
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
.table-wrap {
  overflow-x: auto;
}
</style>
