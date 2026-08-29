<script setup lang="ts">
// The book's own history, drawn as inline SVG. There is no chart library here:
// the scaling arithmetic lives in curveMath.ts so it can be unit-tested without
// a DOM, and what is left below is markup.
import { computed, ref } from 'vue'
import { buildCurveChart, formatIndex, indexAtX, anchorFor, scaleX, scaleY } from '../curveMath'
import type { ChartGeometry, CurveMode } from '../curveMath'
import { formatCents, formatCompactCents, formatSignedCents } from '../money'
import type { CurrencyCurve, Currency, CurvePoint } from '../types'

const props = defineProps<{
  curve: CurrencyCurve
  currency: Currency
}>()

// A fixed coordinate system scaled to the container by the viewBox, so the
// arithmetic never has to know how wide the browser window is. The left margin
// carries the y labels, which is why it is the wide one.
const GEOMETRY: ChartGeometry = {
  width: 900,
  height: 280,
  padding: { top: 16, right: 20, bottom: 30, left: 64 },
}

// Two modes rather than two lines on one axis. Market value and money in share
// a scale because they are the same unit and the gap between them is the gain;
// an index around 100 does not belong on that axis at all, and drawing it there
// would flatten it onto the baseline and imply a relationship that is not there.
const mode = ref<CurveMode>('value')

const svg = ref<SVGSVGElement | null>(null)
// Which session the pointer is nearest, or null when it has left the plot. The
// readout falls back to the last session, so there is always a figure on screen.
const hovered = ref<number | null>(null)

const points = computed(() => props.curve.points)

const chart = computed(() => buildCurveChart(points.value, mode.value, GEOMETRY))

/** The session the readout describes: the hovered one, else the latest. */
const readout = computed<CurvePoint | null>(() => {
  if (points.value.length === 0) return null
  return points.value[hovered.value ?? points.value.length - 1]
})

const isLatest = computed(() => hovered.value === null)

/** The reference line every mode measures against: 0 for money, 100 for the index. */
const anchorY = computed(() => scaleY(anchorFor(mode.value), chart.value.extent, chart.value.plot))

/** Whether the anchor is inside the plot rather than sitting on its frame. */
const anchorVisible = computed(() => {
  const { y, height } = chart.value.plot
  return anchorY.value > y + 1 && anchorY.value < y + height - 1
})

const crosshairX = computed(() =>
  hovered.value === null ? 0 : scaleX(hovered.value, points.value.length, chart.value.plot),
)

/** The marker positions on each line for the hovered session. */
const markers = computed(() => {
  if (hovered.value === null) return []
  return chart.value.lines.map((line) => ({
    key: line.key,
    y: scaleY(line.values[hovered.value as number], chart.value.extent, chart.value.plot),
  }))
})

function onMove(event: PointerEvent) {
  const el = svg.value
  if (!el || points.value.length === 0) return
  // Pointer coordinates are in CSS pixels; the plot is in viewBox units.
  const rect = el.getBoundingClientRect()
  if (rect.width === 0) return
  const x = ((event.clientX - rect.left) / rect.width) * GEOMETRY.width
  hovered.value = indexAtX(x, points.value.length, chart.value.plot)
}

/** Format a y-axis label for whichever mode is showing. */
function axisLabel(value: number): string {
  return mode.value === 'value' ? formatCompactCents(value, props.currency) : formatIndex(value)
}

/** The gain at a session: what the holdings are worth over what was put into them. */
function gainAt(p: CurvePoint): number {
  return p.market_value - p.net_invested
}

function plClass(value: number): string {
  return value < 0 ? 'loss' : 'gain'
}
</script>

<template>
  <div class="chart">
    <div class="chart-head">
      <div class="modes">
        <button class="chip" :class="{ active: mode === 'value' }" @click="mode = 'value'">
          Value
        </button>
        <button
          class="chip"
          :class="{ active: mode === 'performance' }"
          @click="mode = 'performance'"
        >
          Performance
        </button>
      </div>

      <!-- Always a figure on screen: the hovered session, or the latest one. -->
      <div v-if="readout" class="readout">
        <span class="readout-date">
          {{ readout.date }}
          <span v-if="isLatest" class="muted">· latest</span>
        </span>
        <template v-if="mode === 'value'">
          <span class="readout-item">
            <i class="swatch swatch-value" />
            {{ formatCents(readout.market_value, currency) }}
          </span>
          <span class="readout-item">
            <i class="swatch swatch-invested" />
            {{ formatCents(readout.net_invested, currency) }} in
          </span>
          <span class="readout-item" :class="plClass(gainAt(readout))">
            {{ formatSignedCents(gainAt(readout), currency) }}
          </span>
        </template>
        <template v-else>
          <span class="readout-item">
            <i class="swatch swatch-value" />
            {{ formatIndex(readout.index) }}
            <span class="muted">from 100.0</span>
          </span>
        </template>
      </div>
    </div>

    <svg
      ref="svg"
      class="plot"
      :viewBox="`0 0 ${GEOMETRY.width} ${GEOMETRY.height}`"
      preserveAspectRatio="xMidYMid meet"
      role="img"
      :aria-label="`${currency} equity curve, ${mode} mode`"
      @pointermove="onMove"
      @pointerleave="hovered = null"
    >
      <!-- Grid first, so every line and marker sits above it. -->
      <g class="grid">
        <template v-for="t in chart.ticks" :key="t.value">
          <line :x1="chart.plot.x" :y1="t.y" :x2="chart.plot.x + chart.plot.width" :y2="t.y" />
          <text :x="chart.plot.x - 10" :y="t.y + 4" text-anchor="end">{{ axisLabel(t.value) }}</text>
        </template>
      </g>

      <!-- The reference the mode is read against: zero money, or standing still
           at 100. Dashed, so it never reads as a third series. -->
      <line
        v-if="anchorVisible"
        class="anchor"
        :x1="chart.plot.x"
        :y1="anchorY"
        :x2="chart.plot.x + chart.plot.width"
        :y2="anchorY"
      />

      <g class="x-axis">
        <text
          v-for="l in chart.xLabels"
          :key="l.label"
          :x="l.x"
          :y="GEOMETRY.height - 10"
          text-anchor="middle"
        >
          {{ l.label }}
        </text>
      </g>

      <path v-for="line in chart.lines" :key="line.key" :class="['line', line.key]" :d="line.path" />

      <g v-if="hovered !== null" class="crosshair">
        <line
          :x1="crosshairX"
          :y1="chart.plot.y"
          :x2="crosshairX"
          :y2="chart.plot.y + chart.plot.height"
        />
        <circle v-for="m in markers" :key="m.key" :class="m.key" :cx="crosshairX" :cy="m.y" r="4" />
      </g>
    </svg>

    <p v-if="mode === 'value'" class="muted caption">
      Market value against the money actually paid in. The gap between them is the gain — which is
      why they share one scale.
    </p>
    <p v-else class="muted caption">
      A notional 100 chained from the daily returns, with contributions divided out. Adding to a
      holding moves this line only by how the new shares then perform, so saving harder cannot look
      like skill.
    </p>
  </div>
</template>

<style scoped>
.chart {
  background: #fff;
  border-radius: 12px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.08);
  padding: 14px 16px 10px;
}
.chart-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.modes {
  display: flex;
  gap: 6px;
}
.chip {
  width: auto;
  background: #f1f5f9;
  border: 1px solid #e2e8f0;
  color: #334155;
  padding: 4px 12px;
  border-radius: 999px;
  cursor: pointer;
  font-weight: 500;
  font-size: 13px;
}
.chip:hover {
  background: #e2e8f0;
}
.chip.active {
  background: #0d9488;
  border-color: #0d9488;
  color: #fff;
}
.readout {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
  font-size: 13px;
  font-variant-numeric: tabular-nums;
}
.readout-date {
  color: #6b7280;
}
.readout-item {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-weight: 500;
}
.swatch {
  width: 10px;
  height: 2px;
  border-radius: 1px;
  display: inline-block;
}
.swatch-value {
  background: #0d9488;
}
.swatch-invested {
  background: #94a3b8;
}
.plot {
  width: 100%;
  height: auto;
  display: block;
  touch-action: none;
}
.grid line {
  stroke: #e2e8f0;
  stroke-width: 1;
}
.grid text {
  fill: #94a3b8;
  font-size: 11px;
}
.x-axis text {
  fill: #94a3b8;
  font-size: 11px;
}
.anchor {
  stroke: #cbd5e1;
  stroke-width: 1;
  stroke-dasharray: 4 4;
}
.line {
  fill: none;
  stroke-width: 2;
  stroke-linecap: round;
  stroke-linejoin: round;
}
.line.market_value,
.line.index {
  stroke: #0d9488;
}
/* Money in is the reference the value line is read against, not a result of its
   own, so it is drawn quieter and dashed. */
.line.net_invested {
  stroke: #94a3b8;
  stroke-width: 1.5;
  stroke-dasharray: 5 4;
}
.crosshair line {
  stroke: #94a3b8;
  stroke-width: 1;
  stroke-dasharray: 3 3;
}
.crosshair circle {
  stroke: #fff;
  stroke-width: 2;
}
.crosshair circle.market_value,
.crosshair circle.index {
  fill: #0d9488;
}
.crosshair circle.net_invested {
  fill: #94a3b8;
}
.caption {
  font-size: 12px;
  line-height: 1.5;
  margin: 6px 0 0;
  max-width: 78ch;
}
</style>
