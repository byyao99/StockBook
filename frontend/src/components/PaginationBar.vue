<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  limit: number
  offset: number
  total: number
}>()

const emit = defineEmits<{ (e: 'change', offset: number): void }>()

const from = computed(() => (props.total === 0 ? 0 : props.offset + 1))
const to = computed(() => Math.min(props.offset + props.limit, props.total))
const canPrev = computed(() => props.offset > 0)
const canNext = computed(() => props.offset + props.limit < props.total)

function prev() {
  if (canPrev.value) emit('change', Math.max(0, props.offset - props.limit))
}
function next() {
  if (canNext.value) emit('change', props.offset + props.limit)
}
</script>

<template>
  <div class="pager" v-if="total > 0">
    <span class="muted">Showing {{ from }}–{{ to }} of {{ total }}</span>
    <div class="pager-buttons">
      <button class="btn-secondary" :disabled="!canPrev" @click="prev">Prev</button>
      <button class="btn-secondary" :disabled="!canNext" @click="next">Next</button>
    </div>
  </div>
</template>

<style scoped>
.pager {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 12px;
}
.pager-buttons {
  display: flex;
  gap: 8px;
}
button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
