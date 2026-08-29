<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { authApi, settingsApi } from '../api/client'
import { currentUser, setSession } from '../session'
import { FEE_PROFILE_LABELS, chargeMode, effectiveRatePpm, profileCurrency } from '../feeMath'
import type { FeeChargeMode } from '../feeMath'
import {
  bpsToZhe,
  currencySymbol,
  formatCents,
  formatPpmPercent,
  fromCents,
  percentToPpm,
  ppmToPercent,
  toCents,
  zheToBps,
} from '../money'
import type { FeeProfile, FeeProfileKey } from '../types'

const oldPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const error = ref('')
const success = ref('')
const loading = ref(false)

async function submit() {
  error.value = ''
  success.value = ''
  if (newPassword.value !== confirmPassword.value) {
    error.value = 'New passwords do not match.'
    return
  }
  loading.value = true
  try {
    // The change revokes the current token; adopt the fresh session from the
    // response so the user stays signed in.
    const session = await authApi.changePassword(oldPassword.value, newPassword.value)
    setSession(session.token, session.user)
    success.value = 'Password changed successfully.'
    oldPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

// Fee settings keep their own error/success/loading refs rather than sharing the
// password form's: two independent forms on one page would otherwise overwrite
// each other's messages, and a "Saved" under the password fields would be a lie.
const feeRows = ref<FeeRow[]>([])
const feeError = ref('')
const feeSuccess = ref('')
const feeLoading = ref(false)
const feeSaving = ref(false)

/**
 * One profile as the form edits it. Rates are shown as percentages and amounts
 * as whole currency units, because that is how a brokerage quotes them; the
 * conversion to the stored ppm and minor units happens here at the UI edge and
 * nowhere else, exactly as `money.ts` does for every other amount.
 */
interface FeeRow {
  key: FeeProfileKey
  // Whether this broker quotes a percentage or a fixed charge per trade. It is
  // derived from the saved numbers rather than stored (see `chargeMode`), and
  // held on the row only so switching it can clear the inputs the other mode
  // does not use.
  mode: FeeChargeMode
  ratePercent: number
  // The fixed amount, in whole currency units: a floor on a percentage charge,
  // or the whole charge when the mode is flat. Both are the same stored
  // `min_fee` because the formula treats them identically.
  amountUnits: number
  sellTaxPercent: number
  // The broker's discount in 折, or null for none. It is edited apart from the
  // rate because that is how it is quoted — a discount off the standard
  // commission, not a rate of its own — and an empty field says "full price"
  // more plainly than a 10 would.
  discountZhe: number | null
}

function toRow(p: FeeProfile): FeeRow {
  return {
    key: p.key,
    mode: chargeMode(p),
    ratePercent: ppmToPercent(p.rate_ppm),
    amountUnits: fromCents(p.min_fee),
    sellTaxPercent: ppmToPercent(p.sell_tax_ppm),
    discountZhe: p.discount_bps > 0 ? bpsToZhe(p.discount_bps) : null,
  }
}

/**
 * What a row actually charges, shown beside the inputs so the discount's effect
 * is visible while it is being typed rather than only once a trade is entered.
 */
function effectiveRateLabel(row: FeeRow): string {
  return formatPpmPercent(
    effectiveRatePpm({
      key: row.key,
      rate_ppm: percentToPpm(row.ratePercent || 0),
      min_fee: 0,
      sell_tax_ppm: 0,
      discount_bps: row.discountZhe ? zheToBps(row.discountZhe) : 0,
    }),
  )
}

/**
 * What the row charges, in the terms it is quoted in: an effective rate, or the
 * fixed amount itself. Shown beside the inputs so a discount's effect — or the
 * absence of one — is visible while it is being typed.
 */
function chargedLabel(row: FeeRow): string {
  if (row.mode === 'flat') {
    return `${formatCents(toCents(row.amountUnits || 0), profileCurrency(row.key))} / trade`
  }
  return effectiveRateLabel(row)
}

/**
 * Clears what the mode being left behind was carrying. A flat charge has no
 * rate and nothing to discount, so both are zeroed on the way in; the amount is
 * kept in either direction, since it is the same field playing two parts and a
 * number the user typed should not vanish because they changed their mind.
 */
function applyMode(row: FeeRow) {
  if (row.mode === 'flat') {
    row.ratePercent = 0
    row.discountZhe = null
  }
}

async function loadFees() {
  feeLoading.value = true
  feeError.value = ''
  try {
    feeRows.value = (await settingsApi.feeProfiles()).map(toRow)
  } catch (e) {
    feeError.value = (e as Error).message
  } finally {
    feeLoading.value = false
  }
}

async function saveFees() {
  feeError.value = ''
  feeSuccess.value = ''
  feeSaving.value = true
  try {
    const saved = await settingsApi.saveFeeProfiles(
      // A flat charge is stored as a zero rate with the amount in `min_fee`,
      // which is what the shared formula already computes for every trade size.
      // Nothing needs a separate column, and nothing can drift out of step.
      feeRows.value.map((r) => ({
        key: r.key,
        rate_ppm: r.mode === 'flat' ? 0 : percentToPpm(r.ratePercent),
        min_fee: toCents(r.amountUnits),
        sell_tax_ppm: percentToPpm(r.sellTaxPercent),
        discount_bps: r.mode === 'flat' || !r.discountZhe ? 0 : zheToBps(r.discountZhe),
      })),
    )
    // Adopt what came back rather than keeping what was typed: the server is
    // the authority on what is stored, and a rejected value must not linger on
    // screen looking saved.
    feeRows.value = saved.map(toRow)
    feeSuccess.value = 'Fee settings saved.'
  } catch (e) {
    feeError.value = (e as Error).message
  } finally {
    feeSaving.value = false
  }
}

onMounted(loadFees)
</script>

<template>
  <div class="auth-wrap">
    <section class="card auth-card">
      <h2 class="section-title">Account</h2>
      <p v-if="currentUser" class="muted who">
        Signed in as <strong>{{ currentUser.username }}</strong> ({{ currentUser.role }})
      </p>

      <h3 class="sub">Change Password</h3>
      <p v-if="error" class="error">{{ error }}</p>
      <p v-if="success" class="success">{{ success }}</p>

      <form @submit.prevent="submit">
        <div class="field">
          <label>Current password</label>
          <input v-model="oldPassword" type="password" required autocomplete="current-password" />
        </div>
        <div class="field">
          <label>New password</label>
          <input
            v-model="newPassword"
            type="password"
            required
            minlength="8"
            maxlength="72"
            autocomplete="new-password"
          />
        </div>
        <div class="field">
          <label>Confirm new password</label>
          <input
            v-model="confirmPassword"
            type="password"
            required
            minlength="8"
            maxlength="72"
            autocomplete="new-password"
          />
        </div>
        <button type="submit" class="btn-primary full" :disabled="loading">
          {{ loading ? 'Please wait…' : 'Change Password' }}
        </button>
      </form>
      <p class="hint muted">
        Use at least two of: lowercase letter, uppercase letter, digit.
      </p>
    </section>

    <section class="card fee-card">
      <h2 class="section-title">Brokerage Fees</h2>
      <p class="muted intro">
        What your broker charges you. The trade form uses these to fill in the fee
        on a new entry, picking a row from the instrument's market and type — you
        can always change the figure it suggests before saving the trade.
      </p>

      <p v-if="feeError" class="error">{{ feeError }}</p>
      <p v-if="feeSuccess" class="success">{{ feeSuccess }}</p>
      <p v-if="feeLoading" class="muted">Loading…</p>

      <form v-else @submit.prevent="saveFees">
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Trade type</th>
                <th>Charged as</th>
                <th class="num">Commission</th>
                <th class="num">Discount</th>
                <th class="num">Works out to</th>
                <th class="num">Minimum</th>
                <th class="num">Sell tax</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in feeRows" :key="row.key">
                <td>{{ FEE_PROFILE_LABELS[row.key] }}</td>
                <!-- Not every broker quotes a rate: a fixed charge per trade is
                     just as common, and forcing it into a percentage would mean
                     working one out that only happens to be right at one trade
                     size. -->
                <td>
                  <select v-model="row.mode" @change="applyMode(row)">
                    <option value="rate">% of trade</option>
                    <option value="flat">Fixed per trade</option>
                  </select>
                </td>
                <!-- The commission cell holds whichever number the mode charges:
                     a percentage, or the fixed amount. They are the same column
                     because they answer the same question. -->
                <td class="num">
                  <div v-if="row.mode === 'rate'" class="cell">
                    <input v-model.number="row.ratePercent" type="number" min="0" max="10" step="0.0001" />
                    <span class="unit">%</span>
                  </div>
                  <div v-else class="cell">
                    <span class="unit">{{ currencySymbol(profileCurrency(row.key)) }}</span>
                    <input v-model.number="row.amountUnits" type="number" min="0" step="0.01" />
                  </div>
                </td>
                <!-- Blank means full price. Ten 折 is no discount, so the field
                     is left empty rather than pre-filled with a 10 nobody
                     would think to type. A fixed charge has no list rate behind
                     it, so there is nothing to discount. -->
                <td class="num">
                  <div v-if="row.mode === 'rate'" class="cell">
                    <input
                      v-model.number="row.discountZhe"
                      type="number"
                      min="0.1"
                      max="10"
                      step="0.1"
                      placeholder="—"
                      class="narrow"
                    />
                    <span class="unit">折</span>
                  </div>
                  <span v-else class="muted">—</span>
                </td>
                <td class="num charged">{{ chargedLabel(row) }}</td>
                <td class="num">
                  <div v-if="row.mode === 'rate'" class="cell">
                    <span class="unit">{{ currencySymbol(profileCurrency(row.key)) }}</span>
                    <input v-model.number="row.amountUnits" type="number" min="0" step="1" />
                  </div>
                  <span v-else class="muted">—</span>
                </td>
                <td class="num">
                  <div class="cell">
                    <input v-model.number="row.sellTaxPercent" type="number" min="0" max="10" step="0.0001" />
                    <span class="unit">%</span>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <button type="submit" class="btn-primary" :disabled="feeSaving">
          {{ feeSaving ? 'Saving…' : 'Save Fee Settings' }}
        </button>
      </form>

      <p class="hint muted left">
        A row charges either a percentage of the trade or a fixed amount per
        trade — set whichever your broker quotes. On a percentage row, the
        discount is what your broker takes off the listed commission: leave it
        blank for full price, or enter 2.8 for a 2.8 折 rate. It applies to the
        commission alone — the minimum is a floor on what you actually pay, and
        the sell tax is set by the government, so neither is discounted.
        A savings plan is a regular scheduled purchase, which usually carries its
        own rate. Sell tax is charged on sales only — in Taiwan it is the
        securities transaction tax, which is 0.1% for an ETF and 0.3% otherwise.
        Dividends are not estimated: what is withheld from a payout is a
        different charge, so that field stays yours to enter.
      </p>
    </section>
  </div>
</template>

<style scoped>
.auth-wrap {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 16px;
}
.auth-card {
  width: 100%;
  max-width: 380px;
}
/* Wider than the password card: the fee table is six rows of a mode select,
   four inputs and a derived column, which does not fit in the 380px the
   password form wants. It scrolls inside .table-wrap below this, so a narrow
   window degrades rather than pushing the page sideways. */
.fee-card {
  width: 100%;
  max-width: 1000px;
}
.who {
  margin-top: -8px;
}
.sub {
  margin: 16px 0 8px;
  font-size: 15px;
}
.success {
  color: #16a34a;
  font-weight: 600;
}
.full {
  width: 100%;
  margin-top: 4px;
}
.hint {
  text-align: center;
  font-size: 12px;
  margin-top: 8px;
}
.hint.left {
  text-align: left;
}
.intro {
  margin-top: -8px;
  font-size: 13px;
}
.table-wrap {
  overflow-x: auto;
}
/* Narrower than a full-width select would be: the column only has to hold two
   short phrases, and the table is already the widest thing on the page. */
td select {
  width: 128px;
  padding: 5px 6px;
  font-size: 13px;
}
.cell {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 4px;
}
.cell input {
  width: 96px;
  text-align: right;
}
.cell input.narrow {
  width: 64px;
}
/* The one figure in the row that is derived rather than entered. */
.charged {
  font-variant-numeric: tabular-nums;
  color: #0d9488;
  font-weight: 600;
  white-space: nowrap;
}
.unit {
  color: #6b7280;
  font-size: 12px;
}
</style>
