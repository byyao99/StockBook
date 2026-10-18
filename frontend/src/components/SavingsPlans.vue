<script setup lang="ts">
/**
 * The caller's savings plans: list, add, edit, stop, delete.
 *
 * A plan is configuration, not ledger — it says what is *supposed* to happen
 * and never that it did. It lives on the Ledger page anyway, beside the prompts
 * it generates, because that is where a reader looks when an instalment is
 * missing or wrong: the prompt and the terms that raised it are one scroll
 * apart instead of two pages. It emits `changed` after every write so the page
 * can recompute those prompts.
 *
 * An edit changes the days, the amount and the start date in place. The
 * instrument is fixed: a plan's instalments are matched to buys of its
 * instrument, so pointing it elsewhere would orphan every purchase it already
 * explained. That is a new plan, and the server refuses to do it any other way.
 */
import { onMounted, ref } from 'vue'
import { planApi } from '../api/client'
import { formatCents, fromCents, toCents } from '../money'
import type { Instrument, RecurringPlan } from '../types'

defineProps<{
  /** Instruments a new plan can buy — the ledger already holds this list. */
  instruments: Instrument[]
}>()

const emit = defineEmits<{ (e: 'changed'): void }>()

const plans = ref<RecurringPlan[]>([])
const error = ref('')
const success = ref('')

const form = ref({
  instrument_id: '',
  days_of_month: '',
  amountDollars: null as number | null,
  started_on: new Date().toISOString().slice(0, 10),
})

// One row at a time is edited, inline, so the instrument it buys stays in view
// beside the terms being changed.
const editingId = ref<string | null>(null)
const edit = ref({ days_of_month: '', amountDollars: null as number | null, started_on: '' })

async function load() {
  try {
    plans.value = await planApi.list()
  } catch (e) {
    error.value = (e as Error).message
  }
}

function clearMessages() {
  error.value = ''
  success.value = ''
}

async function addPlan() {
  clearMessages()
  try {
    await planApi.create({
      instrument_id: form.value.instrument_id,
      days_of_month: form.value.days_of_month,
      amount: toCents(form.value.amountDollars ?? 0),
      started_on: form.value.started_on,
    })
    form.value.instrument_id = ''
    form.value.days_of_month = ''
    form.value.amountDollars = null
    success.value = 'Plan added. Any instalments it has already come due for are listed at the top of this page.'
    await load()
    emit('changed')
  } catch (e) {
    error.value = (e as Error).message
  }
}

function startEdit(plan: RecurringPlan) {
  clearMessages()
  editingId.value = plan.id
  edit.value = {
    days_of_month: plan.days_of_month,
    amountDollars: fromCents(plan.amount),
    started_on: plan.started_on,
  }
}

function cancelEdit() {
  editingId.value = null
}

async function saveEdit(plan: RecurringPlan) {
  clearMessages()
  try {
    await planApi.update(plan.id, {
      days_of_month: edit.value.days_of_month,
      amount: toCents(edit.value.amountDollars ?? 0),
      started_on: edit.value.started_on,
    })
    editingId.value = null
    success.value = `Updated the ${plan.symbol} plan. Instalments still waiting to be recorded follow the new terms; recorded purchases are unchanged.`
    await load()
    emit('changed')
  } catch (e) {
    error.value = (e as Error).message
  }
}

// Stopping keeps the plan: the purchases it prompted are still in the ledger,
// and it is the only record of why they are spaced as they are.
async function endPlan(plan: RecurringPlan) {
  clearMessages()
  try {
    await planApi.end(plan.id)
    success.value = `Stopped the ${plan.symbol} plan. It stays here for the record.`
    await load()
    emit('changed')
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function removePlan(plan: RecurringPlan) {
  if (!confirm(`Delete the ${plan.symbol} plan entirely? Use Stop to keep it for the record.`)) {
    return
  }
  clearMessages()
  try {
    await planApi.remove(plan.id)
    success.value = 'Plan deleted.'
    await load()
    emit('changed')
  } catch (e) {
    error.value = (e as Error).message
  }
}

onMounted(load)
</script>

<template>
  <section class="card">
    <h2 class="section-title">Savings Plans</h2>
    <p class="muted intro">
      What is <em>supposed</em> to happen — a standing instruction to buy a fixed
      amount on given days of the month. A plan never writes an entry:
      instalments it has come due for appear at the top of this page, where you
      confirm what actually executed.
    </p>

    <p v-if="error" class="error">{{ error }}</p>
    <p v-if="success" class="success">{{ success }}</p>

    <div v-if="plans.length > 0" class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Instrument</th>
            <th>Days</th>
            <th class="num">Each time</th>
            <th>From</th>
            <th>Status</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="p in plans"
            :key="p.id"
            :class="{ closed: p.ended_on !== '' && editingId !== p.id }"
          >
            <td>
              <strong>{{ p.symbol }}</strong>
              <div class="muted">{{ p.name }}</div>
            </td>
            <template v-if="editingId === p.id">
              <td><input v-model="edit.days_of_month" class="edit-days" placeholder="5,15,25" /></td>
              <td class="num">
                <input
                  v-model.number="edit.amountDollars"
                  class="edit-amount"
                  type="number"
                  step="0.01"
                  min="0"
                />
              </td>
              <td><input v-model="edit.started_on" type="date" /></td>
            </template>
            <template v-else>
              <td>{{ p.days_of_month }}</td>
              <td class="num">{{ formatCents(p.amount, p.currency) }}</td>
              <td>{{ p.started_on }}</td>
            </template>
            <td>
              <span v-if="p.ended_on === ''" class="badge badge-buy">running</span>
              <span v-else class="muted">stopped {{ p.ended_on }}</span>
            </td>
            <td>
              <!-- The flex row lives inside the cell, not on it: a td made a
                   flex container stops being a table cell, and its border
                   then no longer lines up with the rest of the row. -->
              <div class="plan-actions">
                <template v-if="editingId === p.id">
                  <button class="btn-primary" @click="saveEdit(p)">Save</button>
                  <button class="btn-secondary" @click="cancelEdit">Cancel</button>
                </template>
                <template v-else>
                  <button class="btn-secondary" @click="startEdit(p)">Edit</button>
                  <button v-if="p.ended_on === ''" class="btn-secondary" @click="endPlan(p)">
                    Stop
                  </button>
                  <button class="btn-danger" @click="removePlan(p)">Delete</button>
                </template>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-else class="muted">No savings plan yet.</p>
    <p v-if="editingId" class="muted hint">
      An edit corrects the plan's whole history: instalments not yet recorded are
      re-estimated from the new terms. If your broker changed the plan from a
      date onward, Stop this one and add a new plan starting then instead, so the
      earlier instalments keep their old amount. The instrument cannot be
      changed — that is a different plan.
    </p>

    <form class="plan-form" @submit.prevent="addPlan">
      <div class="field">
        <label>Instrument</label>
        <select v-model="form.instrument_id" required>
          <option value="" disabled>Choose…</option>
          <option v-for="i in instruments" :key="i.id" :value="i.id">
            {{ i.symbol }} — {{ i.name }}
          </option>
        </select>
      </div>
      <div class="field">
        <label>Days of month</label>
        <input v-model="form.days_of_month" placeholder="5,15,25" required />
        <span class="muted hint">
          A day past the end of a short month runs on its last day rather than
          being skipped.
        </span>
      </div>
      <div class="field">
        <label>Amount each time</label>
        <input v-model.number="form.amountDollars" type="number" step="0.01" min="0" required />
        <span class="muted hint">
          The cash debited, which is what a plan actually fixes — the shares
          follow from the morning's price.
        </span>
      </div>
      <div class="field">
        <label>Started on</label>
        <input v-model="form.started_on" type="date" required />
      </div>
      <!-- The button has no label of its own, and an empty one is what gives it
           exactly the height the labelled fields spend above their inputs — so
           it lands on the same line as them at any width. -->
      <div class="field">
        <label aria-hidden="true">&nbsp;</label>
        <button class="btn-primary" type="submit">Add plan</button>
      </div>
    </form>
  </section>
</template>

<style scoped>
.intro {
  margin-top: -8px;
  font-size: 13px;
}
.success {
  color: #16a34a;
  font-weight: 600;
}
.hint {
  display: block;
  font-size: 12px;
  margin-top: 4px;
}
.table-wrap {
  overflow-x: auto;
}
/* Aligned at the top, not the bottom: the hints under these fields are of very
   different lengths, so a field's height says nothing about where its label
   belongs. Every label is one line, so starting them together lands the
   controls together too, whatever hangs below. */
.plan-form {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px;
  align-items: start;
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid #f1f5f9;
}
.plan-form .field {
  margin-bottom: 0;
}
.plan-form button {
  width: 100%;
}
.plan-actions {
  display: flex;
  gap: 6px;
  justify-content: flex-end;
}
.plan-actions button {
  width: auto;
  padding: 4px 10px;
  font-size: 13px;
}
/* Sized to what they hold, so an edited row stays the width of a read one. */
.edit-days {
  width: 96px;
}
.edit-amount {
  width: 110px;
  text-align: right;
}
td input {
  padding: 5px 6px;
  font-size: 13px;
}
/* A stopped plan is kept for the record, so it is dimmed rather than removed. */
tr.closed {
  opacity: 0.55;
}
</style>
