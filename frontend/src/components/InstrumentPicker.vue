<script setup lang="ts">
/**
 * Picks the instrument a trade is against, by search.
 *
 * There is no separate "add an instrument" step. An instrument exists only to
 * be traded, so the only moment anyone needs one is while recording a trade,
 * and making that a second action turns picking a stock into managing master
 * data. Typing here searches what has already been traded first, then the price
 * provider for everything else; choosing a result that is not on file yet
 * creates it on the way through, which is invisible except that the trade form
 * fills in immediately afterwards.
 *
 * Local matches are filtered client-side and appear instantly, because trading
 * the same handful of stocks repeatedly is the common case and should not wait
 * on a network round trip. The provider is queried only after a pause in typing,
 * and only for what the local list does not already cover.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { instrumentApi } from '../api/client'
import type { Instrument, InstrumentCandidate } from '../types'

const props = defineProps<{
  /** Instruments already on file; the fast path for anything traded before. */
  instruments: Instrument[]
  /** The currently chosen instrument id, or '' for none. */
  modelValue: string
  /** Locks the picker — an entry's instrument cannot move once recorded. */
  disabled?: boolean
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', id: string): void
  /** Fired when a pick created an instrument, so the parent can reload its list. */
  (e: 'created'): void
  (e: 'error', message: string): void
}>()

const query = ref('')
const open = ref(false)
const candidates = ref<InstrumentCandidate[]>([])
const searching = ref(false)
const adding = ref('')

const selected = computed(() => props.instruments.find((i) => i.id === props.modelValue))

/** Instruments already on file whose symbol or name matches what is typed. */
const localMatches = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return props.instruments.slice(0, 8)
  return props.instruments
    .filter((i) => i.symbol.toLowerCase().includes(q) || i.name.toLowerCase().includes(q))
    .slice(0, 8)
})

/**
 * Provider results for listings not already on file. `exists` comes from the
 * server, and the local ids are checked too so a just-added instrument does not
 * momentarily appear in both halves of the list.
 */
const remoteMatches = computed(() =>
  candidates.value.filter(
    (c) => !c.exists && !props.instruments.some((i) => i.symbol === c.symbol && i.market === c.market),
  ),
)

let debounce: ReturnType<typeof setTimeout> | undefined

// The provider is a rate-limited third party, so it is asked only after typing
// settles, and never for a fragment too short to mean anything.
watch(query, (q) => {
  clearTimeout(debounce)
  candidates.value = []
  if (q.trim().length < 2) return
  debounce = setTimeout(() => void searchProvider(q.trim()), 400)
})

onBeforeUnmount(() => clearTimeout(debounce))

async function searchProvider(q: string) {
  searching.value = true
  try {
    const found = await instrumentApi.search(q)
    // Discard a response that arrived after the box moved on.
    if (query.value.trim() === q) candidates.value = found
  } catch (e) {
    emit('error', (e as Error).message)
  } finally {
    searching.value = false
  }
}

function choose(instrument: Instrument) {
  emit('update:modelValue', instrument.id)
  close()
}

/**
 * Chooses a listing that is not on file yet, creating it first. The server
 * prices it as part of creating it, so the trade can be recorded straight away
 * and the holding valued the moment it is.
 */
async function chooseCandidate(candidate: InstrumentCandidate) {
  adding.value = candidate.ticker
  try {
    const created = await instrumentApi.create({
      symbol: candidate.symbol,
      market: candidate.market,
    })
    emit('created')
    emit('update:modelValue', created.id)
    close()
  } catch (e) {
    emit('error', (e as Error).message)
  } finally {
    adding.value = ''
  }
}

function close() {
  open.value = false
  query.value = ''
  candidates.value = []
}

function clear() {
  emit('update:modelValue', '')
  open.value = true
}
</script>

<template>
  <div class="picker">
    <!-- Once something is chosen the box collapses to it: the search was a way
         to reach an instrument, not a field worth keeping on screen. -->
    <div v-if="selected && !open" class="chosen" :class="{ locked: disabled }">
      <span class="chosen-symbol">{{ selected.symbol }}</span>
      <span class="chosen-name">{{ selected.name }}</span>
      <span class="muted chosen-meta">{{ selected.market }} · {{ selected.currency }}</span>
      <button v-if="!disabled" type="button" class="link-button" @click="clear">change</button>
    </div>

    <div v-else class="search">
      <input
        v-model="query"
        type="search"
        :disabled="disabled"
        placeholder="Search a stock number or name — e.g. 2330, TSLA"
        @focus="open = true"
      />

      <ul v-if="open" class="results">
        <li v-for="i in localMatches" :key="i.id">
          <button type="button" class="result" @click="choose(i)">
            <span class="sym">{{ i.symbol }}</span>
            <span class="nm">{{ i.name }}</span>
            <span class="muted meta">{{ i.market }} · {{ i.currency }}</span>
          </button>
        </li>

        <li v-if="remoteMatches.length > 0" class="divider">
          <span class="muted">not traded yet — choosing one adds it</span>
        </li>
        <li v-for="c in remoteMatches" :key="c.ticker">
          <button
            type="button"
            class="result"
            :disabled="adding !== ''"
            @click="chooseCandidate(c)"
          >
            <span class="sym">{{ c.symbol }}</span>
            <span class="nm">{{ c.name }}</span>
            <span class="muted meta">
              {{ adding === c.ticker ? 'adding…' : `${c.market} · ${c.currency}` }}
            </span>
          </button>
        </li>

        <li v-if="searching" class="status muted">Searching…</li>
        <li
          v-else-if="query.trim().length >= 2 && localMatches.length === 0 && remoteMatches.length === 0"
          class="status muted"
        >
          Nothing found. Only TWSE, TPEX, NYSE and NASDAQ listings can be
          priced; Chinese names are not matched, so try the stock number.
        </li>
        <li v-else-if="query.trim().length < 2 && localMatches.length === 0" class="status muted">
          Type a stock number or company name.
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.picker {
  position: relative;
}
.chosen {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 9px 12px;
  border: 1px solid #d1d5db;
  border-radius: 8px;
  background: #fff;
  min-height: 40px;
}
.chosen.locked {
  background: #f3f4f6;
}
.chosen-symbol {
  font-weight: 700;
}
.chosen-name {
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chosen-meta {
  font-size: 12px;
  margin-left: auto;
  white-space: nowrap;
}
.link-button {
  width: auto;
  background: none;
  border: none;
  padding: 0;
  color: #0d9488;
  font-size: 12px;
  cursor: pointer;
  text-decoration: underline;
}
.results {
  list-style: none;
  margin: 4px 0 0;
  padding: 4px;
  position: absolute;
  z-index: 20;
  width: 100%;
  max-height: 320px;
  overflow-y: auto;
  background: #fff;
  border: 1px solid #d1d5db;
  border-radius: 8px;
  box-shadow: 0 8px 20px rgba(0, 0, 0, 0.12);
}
.result {
  display: grid;
  grid-template-columns: minmax(56px, auto) 1fr auto;
  align-items: baseline;
  gap: 10px;
  width: 100%;
  text-align: left;
  background: none;
  border: none;
  padding: 8px 10px;
  border-radius: 6px;
  cursor: pointer;
  font: inherit;
}
.result:hover:not(:disabled) {
  background: #f0fdfa;
}
.result:disabled {
  opacity: 0.6;
  cursor: default;
}
.sym {
  font-weight: 700;
}
.nm {
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.meta {
  font-size: 12px;
  white-space: nowrap;
}
.divider {
  padding: 8px 10px 4px;
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  border-top: 1px solid #f0f0f0;
  margin-top: 4px;
}
.status {
  padding: 10px;
  font-size: 13px;
}
</style>
