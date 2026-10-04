<script setup lang="ts">
/**
 * Splits this book held shares across.
 *
 * This is a **warning, not a prompt**, and the difference decides everything
 * about how it reads. The pending-dividend prompt beside it on the Ledger page
 * hands the user a form and a thing to do. There is no entry that settles a
 * split: this system deliberately does not model them, so the ledger goes on
 * recording the shares as they were bought while every stored close and the
 * last price are stated in post-split shares all the way back. Every figure
 * spanning the date is therefore wrong by the ratio, and nothing else on screen
 * says so.
 *
 * So what it offers instead is the arithmetic — what the recorded count
 * actually describes in today's shares — and a way to stop being told. It has
 * to be dismissible, because the condition that raises it (shares were held
 * across that date) stays true forever no matter what the reader does about it,
 * and a warning nobody can clear is one everybody learns to scroll past. It has
 * to be reversible for the opposite reason: a misclick would otherwise silence
 * a genuinely broken history for good.
 */
import { ref } from 'vue'
import { reportApi } from '../api/client'
import { formatShares } from '../shares'
import { formatSharesAfterSplit, formatSplitRatio } from '../splits'
import type { UnadjustedSplit } from '../types'

const props = defineProps<{
  splits: UnadjustedSplit[]
  /** Whether to name the instrument. A holding's own page already has. */
  showSymbol?: boolean
}>()

const emit = defineEmits<{ (e: 'changed'): void }>()

const busy = ref('')
const error = ref('')

function keyOf(s: UnadjustedSplit): string {
  return `${s.instrument_id}|${s.date}`
}

async function toggle(split: UnadjustedSplit) {
  busy.value = keyOf(split)
  error.value = ''
  try {
    if (split.acknowledged) {
      await reportApi.forgetSplitAcknowledgement(split.instrument_id, split.date)
    } else {
      await reportApi.acknowledgeSplit(split.instrument_id, split.date)
    }
    emit('changed')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <div v-if="props.splits.length > 0" class="splits">
    <p v-if="error" class="error">{{ error }}</p>
    <div v-for="s in props.splits" :key="keyOf(s)" class="split" :class="{ dimmed: s.acknowledged }">
      <div class="split-body">
        <p class="split-head">
          <strong v-if="props.showSymbol">{{ s.symbol }}</strong>
          <span class="badge badge-warn">{{ formatSplitRatio(s.ratio_ppm) }} split</span>
          <span class="muted">ex-date {{ s.date }}</span>
        </p>
        <!-- The arithmetic, stated rather than applied. Applying it would mean
             rewriting entries the user never made, which is the one thing this
             system never does to a ledger. -->
        <p class="split-detail">
          Your ledger records
          <strong>{{ formatShares(s.shares) }}</strong>
          shares held the session before, which is
          <strong>{{ formatSharesAfterSplit(s.shares, s.ratio_ppm) }}</strong>
          shares of today's stock. Prices before this date are already stated in the new shares and
          your entries are not, so every figure spanning it — market value, the curve, unrealized
          profit — is out by the ratio until the entries are restated by hand.
        </p>
      </div>
      <button class="btn-secondary split-action" :disabled="busy === keyOf(s)" @click="toggle(s)">
        {{ s.acknowledged ? 'Show again' : 'Dismiss' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.splits {
  margin-bottom: 16px;
}
.split {
  display: flex;
  align-items: flex-start;
  gap: 16px;
  background: #fef2f2;
  border: 1px solid #fecaca;
  color: #7f1d1d;
  border-radius: 8px;
  padding: 12px 14px;
  margin-bottom: 8px;
  font-size: 14px;
}
/* A dismissed warning is still a warning, so it keeps its colour and only
   steps back — the reader is looking at it on purpose. */
.split.dimmed {
  opacity: 0.6;
}
.split-body {
  flex: 1;
}
.split-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 0 4px;
}
.split-detail {
  margin: 0;
  line-height: 1.5;
}
.split-action {
  width: auto;
  white-space: nowrap;
  flex-shrink: 0;
}
</style>
