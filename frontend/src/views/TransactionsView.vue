<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { instrumentApi, settingsApi, transactionApi } from '../api/client'
import {
  bpsToListPercent,
  formatCents,
  formatPpmPercent,
  formatQty,
  formatSignedOrUnknown,
  fromCents,
  toCents,
} from '../money'
import {
  FEE_PROFILE_LABELS,
  chargeMode,
  defaultProfileKey,
  effectiveRatePpm,
  estimateFee,
  findProfile,
  profileCurrency,
} from '../feeMath'
import PaginationBar from '../components/PaginationBar.vue'
import InstrumentPicker from '../components/InstrumentPicker.vue'
import { FEE_PROFILE_KEYS, SIDES } from '../types'
import type {
  Currency,
  FeeProfile,
  FeeProfileKey,
  Instrument,
  Transaction,
  TransactionSide,
} from '../types'

const PAGE_SIZE = 20

const entries = ref<Transaction[]>([])
const instruments = ref<Instrument[]>([])
const feeProfiles = ref<FeeProfile[]>([])
const total = ref(0)
const offset = ref(0)
const loading = ref(false)
const error = ref('')
const success = ref('')

const instrumentFilter = ref('')
const sideFilter = ref<TransactionSide | ''>('')

const editingId = ref<string | null>(null)

/**
 * Whether the user has typed into the price or fee field themselves.
 *
 * Once either is true nothing auto-fills it again. A figure copied off a
 * contract note is always more correct than an estimate, and silently
 * overwriting it as the form recomputes would be both wrong and maddening.
 */
const priceTouched = ref(false)
const feeTouched = ref(false)

// The form edits dollars; the API speaks cents. The conversion happens here at
// the UI edge and nowhere else.
//
// The three numeric fields start null rather than 0 so they render empty. A
// zero has to be deleted before anything can be typed over it, which is a small
// friction paid on every single entry, and a fee left at 0 is a claim that the
// trade was free.
const form = reactive<{
  instrumentId: string
  side: TransactionSide
  quantity: number | null
  priceDollars: number | null
  feeDollars: number | null
  feeProfileKey: FeeProfileKey | ''
  recurring: boolean
  tradedAt: string
  note: string
}>({
  instrumentId: '',
  side: 'buy',
  quantity: null,
  priceDollars: null,
  feeDollars: null,
  feeProfileKey: '',
  recurring: false,
  tradedAt: today(),
  note: '',
})

function today(): string {
  return new Date().toISOString().slice(0, 10)
}

/**
 * Turns the chosen date into the instant the entry is stored at.
 *
 * A bare date is sent as midday UTC so that a timezone shift cannot push the
 * trade into a different day than the one picked. But noon UTC is 20:00 in
 * Taipei, so a trade entered today at any normal hour would be stamped hours
 * ahead of now, and the server refuses a future entry outright — which made
 * today's trades unrecordable before the evening.
 *
 * Clamping to now fixes that without reintroducing the drift, because the date
 * input is capped at the UTC date: whenever noon UTC is still ahead, now is
 * necessarily on that same UTC day, so the entry keeps the day it was given.
 */
function tradedAtInstant(date: string): string {
  const noon = new Date(`${date}T12:00:00Z`)
  const now = new Date()
  return (noon > now ? now : noon).toISOString()
}

const isDividend = computed(() => form.side === 'dividend')

const selectedInstrument = computed(() => instruments.value.find((i) => i.id === form.instrumentId))

// A preview of the cash movement, computed the same way the server will. It is
// shown for the user's benefit only — the stored figure is always the server's.
const netPreview = computed(() => {
  const gross = (form.quantity ?? 0) * toCents(form.priceDollars ?? 0)
  const fee = toCents(form.feeDollars ?? 0)
  return form.side === 'buy' ? gross + fee : gross - fee
})

const previewLabel = computed(() => {
  if (form.side === 'buy') return 'Total cost'
  return isDividend.value ? 'Payout received' : 'Proceeds'
})

// The currency of whichever instrument the form is pointed at, so amounts are
// labelled correctly rather than all wearing a bare "$".
const formCurrency = computed<Currency | undefined>(() => selectedInstrument.value?.currency)

// Ledger rows carry only an instrument id, so the currency is looked up from
// the loaded master data; an instrument since deleted simply shows unlabelled.
function currencyOf(instrumentId: string): Currency | undefined {
  return instruments.value.find((i) => i.id === instrumentId)?.currency
}

/**
 * Suggests the price a trade filled at, which is only knowable for one today.
 *
 * `last_price` is the instrument's current quote, so it answers "what is this
 * worth now?" and nothing else. Offering it for a back-dated entry would write
 * today's price into a trade made last year — the same silent corruption as
 * leaving the fee at zero, and harder to spot afterwards. The field is cleared
 * instead, and the hint asks for the figure off the contract note.
 */
function applyPriceSuggestion() {
  if (editingId.value || priceTouched.value) return
  const inst = selectedInstrument.value
  if (!inst || inst.last_price === null || form.tradedAt !== today()) {
    form.priceDollars = null
    return
  }
  form.priceDollars = fromCents(inst.last_price)
}

function applyFeeEstimate() {
  if (editingId.value || feeTouched.value) return
  const estimate = estimateFee(
    findProfile(feeProfiles.value, form.feeProfileKey),
    form.side,
    form.quantity ?? 0,
    toCents(form.priceDollars ?? 0),
  )
  form.feeDollars = estimate === null ? null : fromCents(estimate)
}

/** Puts the fee back under the profile's control after a hand-typed figure. */
function recalculateFee() {
  feeTouched.value = false
  applyFeeEstimate()
}

// Choosing a different instrument starts the numbers over: the previous
// suggestion belonged to a different holding, and a price carried across from
// one stock to another is worse than an empty field.
watch(
  () => form.instrumentId,
  () => {
    priceTouched.value = false
    feeTouched.value = false
  },
)

// Which profile applies follows from the instrument, the side and whether this
// is a scheduled purchase. It is only ever a default — the select stays live so
// an account on unusual terms, or an ETF the provider called an ordinary share,
// can be corrected in place.
watch(
  [() => form.instrumentId, () => form.side, () => form.recurring],
  () => {
    if (editingId.value) return
    form.feeProfileKey = defaultProfileKey(selectedInstrument.value, form.recurring) ?? ''
  },
)

watch([() => form.instrumentId, () => form.tradedAt], applyPriceSuggestion)

watch(
  [() => form.feeProfileKey, () => form.quantity, () => form.priceDollars, () => form.side],
  applyFeeEstimate,
)

/** What the fee field is currently doing, shown under it. */
const feeHint = computed(() => {
  if (editingId.value) return ''
  if (isDividend.value) return 'Withholding is not estimated — enter what was deducted.'
  if (feeTouched.value) return 'Entered by hand.'
  const profile = findProfile(feeProfiles.value, form.feeProfileKey)
  if (!profile) return 'No fee profile applies — enter the fee by hand.'
  // The terms quoted here are the ones actually charged, after any discount.
  // The list rate would not explain the number in the field beside it, and a
  // profile charging a fixed amount has no meaningful rate to quote at all —
  // rendering its 0% would read as a free trade beside a fee that is not.
  let rate = FEE_PROFILE_LABELS[profile.key]
  if (chargeMode(profile) === 'flat') {
    rate += ` ${formatCents(profile.min_fee, profileCurrency(profile.key))} a trade`
  } else {
    rate += ` ${formatPpmPercent(effectiveRatePpm(profile))}`
    if (profile.discount_bps > 0 && profile.discount_bps < 10000) {
      rate += ` (${formatPpmPercent(profile.rate_ppm)} at ${bpsToListPercent(profile.discount_bps)}% of list)`
    }
  }
  if (form.side === 'sell' && profile.sell_tax_ppm > 0) {
    return `${rate} + ${formatPpmPercent(profile.sell_tax_ppm)} sell tax`
  }
  return rate
})

/** What the price field is currently doing, shown under it. */
const priceHint = computed(() => {
  if (editingId.value || priceTouched.value) return ''
  const inst = selectedInstrument.value
  if (!inst) return ''
  if (form.tradedAt !== today()) return 'Enter the price this trade filled at.'
  if (inst.last_price === null) return 'No quote on file — enter the price it filled at.'
  const asOf = inst.price_updated_at?.slice(0, 10)
  return asOf ? `Latest quote, ${asOf} — change it if it filled elsewhere.` : 'Latest quote.'
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
  } catch (e) {
    error.value = (e as Error).message
  }
}

// A failure here is deliberately not surfaced as a page error: the fee estimate
// is a convenience, and losing it must not look like the ledger is broken. The
// form falls back to a hand-entered fee, which is what it always was.
async function loadFeeProfiles() {
  try {
    feeProfiles.value = await settingsApi.feeProfiles()
  } catch {
    feeProfiles.value = []
  }
}

function changePage(newOffset: number) {
  offset.value = newOffset
  load()
}

/** The badge colour for an entry kind. A dividend is neither a buy nor a sell. */
function sideBadge(side: TransactionSide): string {
  if (side === 'buy') return 'badge-buy'
  return side === 'sell' ? 'badge-sell' : 'badge-dividend'
}

/** Picks the gain/loss class, or nothing at all when there is no result. */
function plClass(value: number | null): string {
  if (value === null) return 'muted'
  return value < 0 ? 'loss' : 'gain'
}

// Changing a filter restarts paging from the first page.
function applyFilter() {
  offset.value = 0
  load()
}

function resetForm() {
  editingId.value = null
  priceTouched.value = false
  feeTouched.value = false
  Object.assign(form, {
    instrumentId: '',
    side: 'buy',
    quantity: null,
    priceDollars: null,
    feeDollars: null,
    feeProfileKey: '',
    recurring: false,
    tradedAt: today(),
    note: '',
  })
}

function startEdit(t: Transaction) {
  editingId.value = t.id
  // Everything in an existing entry is a recorded fact. Both fields count as
  // touched so no watcher can overwrite what was actually banked with an
  // estimate of what it might have been.
  priceTouched.value = true
  feeTouched.value = true
  Object.assign(form, {
    instrumentId: t.instrument_id,
    side: t.side,
    quantity: t.quantity,
    priceDollars: fromCents(t.price),
    feeDollars: fromCents(t.fee),
    feeProfileKey: '',
    recurring: false,
    tradedAt: t.traded_at.slice(0, 10),
    note: t.note,
  })
}

async function submit() {
  error.value = ''
  success.value = ''
  const tradedAt = tradedAtInstant(form.tradedAt)
  try {
    if (editingId.value) {
      await transactionApi.update(editingId.value, {
        quantity: form.quantity ?? 0,
        price: toCents(form.priceDollars ?? 0),
        fee: toCents(form.feeDollars ?? 0),
        traded_at: tradedAt,
        note: form.note,
      })
      success.value = 'Trade updated and holdings rebuilt.'
    } else {
      await transactionApi.create({
        instrument_id: form.instrumentId,
        side: form.side,
        quantity: form.quantity ?? 0,
        price: toCents(form.priceDollars ?? 0),
        fee: toCents(form.feeDollars ?? 0),
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
  await Promise.all([loadInstruments(), loadFeeProfiles()])
  await load()
})
</script>

<template>
  <div>
    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="success" class="success">{{ success }}</p>

    <section class="card">
      <h2 class="section-title">{{ editingId ? 'Edit Trade' : 'Record a Trade' }}</h2>
      <form @submit.prevent="submit">
        <div class="grid">
          <div class="field instrument-field">
            <label>Instrument</label>
            <!-- The instrument cannot move once recorded: changing it would
                 shift the entry to a different holding, which the API expects
                 as a delete plus a fresh entry. -->
            <InstrumentPicker
              v-model="form.instrumentId"
              :instruments="instruments"
              :disabled="editingId !== null"
              @created="loadInstruments"
              @error="error = $event"
            />
          </div>
          <div class="field">
            <label>Side</label>
            <select v-model="form.side" required :disabled="editingId !== null">
              <option v-for="s in SIDES" :key="s" :value="s">{{ s }}</option>
            </select>
          </div>
          <!-- A dividend uses the same three numbers as a trade, but they mean
               different things: the shares it was paid on, the amount per share,
               and the tax withheld. Relabelling is what makes one form serve
               both without a second, near-identical one. -->
          <div class="field">
            <label>{{ isDividend ? 'Shares held' : 'Shares' }}</label>
            <input v-model.number="form.quantity" type="number" min="1" step="1" required />
          </div>
          <div class="field">
            <label>
              {{ isDividend ? 'Per share' : 'Price' }} ({{ formCurrency ?? '—' }})
            </label>
            <input
              v-model.number="form.priceDollars"
              type="number"
              min="0"
              step="0.01"
              required
              @input="priceTouched = true"
            />
            <p v-if="priceHint" class="field-hint muted">{{ priceHint }}</p>
          </div>
          <div class="field">
            <label>
              {{ isDividend ? 'Tax withheld' : 'Fees &amp; tax' }} ({{ formCurrency ?? '—' }})
            </label>
            <input
              v-model.number="form.feeDollars"
              type="number"
              min="0"
              step="0.01"
              @input="feeTouched = true"
            />
            <p v-if="feeHint" class="field-hint muted">
              {{ feeHint }}
              <button
                v-if="feeTouched && !isDividend && !editingId && form.feeProfileKey"
                type="button"
                class="link-button"
                @click="recalculateFee"
              >
                use the estimate
              </button>
            </p>
            <!-- The basis lives inside this field rather than in a grid cell of
                 its own: it decides the number in the input directly above it,
                 and anywhere else in an auto-fit grid it lands columns away from
                 what it controls. It is a suggestion the user can override — the
                 provider occasionally files an ETF as an ordinary share, and a
                 savings plan is how a trade was made rather than what was
                 traded, so neither is reliably derivable. Hidden for a dividend,
                 whose withholding is a different charge entirely. -->
            <div v-if="!isDividend && !editingId" class="fee-basis">
              <select v-model="form.feeProfileKey" aria-label="Fee basis" @change="recalculateFee">
                <option value="">Enter by hand</option>
                <option v-for="k in FEE_PROFILE_KEYS" :key="k" :value="k">
                  {{ FEE_PROFILE_LABELS[k] }}
                </option>
              </select>
              <label class="checkbox">
                <input v-model="form.recurring" type="checkbox" />
                Scheduled purchase
              </label>
            </div>
          </div>
          <div class="field">
            <label>{{ isDividend ? 'Ex-dividend date' : 'Trade date' }}</label>
            <input v-model="form.tradedAt" type="date" :max="today()" required />
          </div>
        </div>
        <div class="field">
          <label>Note</label>
          <input v-model="form.note" maxlength="500" placeholder="optional" />
        </div>

        <p class="preview muted">
          {{ previewLabel }}:
          <strong>{{ formatCents(netPreview, formCurrency) }}</strong>
          <span class="hint">(the server recomputes this; this is a preview)</span>
        </p>

        <div class="actions">
          <button type="submit" class="btn-primary" :disabled="!form.instrumentId">
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
            <option value="">All entries</option>
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
              <th class="num">Realized</th>
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
                <span :class="['badge', sideBadge(t.side)]">{{ t.side }}</span>
              </td>
              <td class="num">{{ formatQty(t.quantity) }}</td>
              <td class="num">{{ formatCents(t.price, currencyOf(t.instrument_id)) }}</td>
              <td class="num">{{ formatCents(t.fee, currencyOf(t.instrument_id)) }}</td>
              <td class="num">{{ formatCents(t.net_amount, currencyOf(t.instrument_id)) }}</td>
              <!-- A buy has no realized result, so it shows the unknown marker
                   rather than a zero that would read as a break-even trade. -->
              <td class="num" :class="plClass(t.realized_pl)">
                {{ formatSignedOrUnknown(t.realized_pl, currencyOf(t.instrument_id)) }}
              </td>
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
.instrument-field {
  /* The picker's dropdown must escape the form grid rather than be clipped. */
  grid-column: 1 / -1;
}
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px;
  align-items: start;
}
.field-hint {
  margin: 4px 0 0;
  font-size: 11px;
  line-height: 1.4;
}
/* The fee basis and the savings-plan flag share one line under the fee field.
   Both wrap on a narrow column rather than forcing the grid wider. */
.fee-basis {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
}
.fee-basis select {
  flex: 1 1 130px;
  padding: 5px 6px;
  font-size: 12px;
}
.fee-basis .checkbox {
  margin-top: 0;
}
.checkbox {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
  font-size: 12px;
  font-weight: 400;
}
.checkbox input {
  width: auto;
  margin: 0;
}
.link-button {
  width: auto;
  background: none;
  border: none;
  padding: 0;
  color: #0d9488;
  font-size: 11px;
  cursor: pointer;
  text-decoration: underline;
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
