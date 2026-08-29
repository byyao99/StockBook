// The arithmetic behind the equity curve chart.
//
// It lives apart from EquityCurveChart.vue so it can be unit-tested without a
// DOM: there is no chart library here, so this scaling is the whole of what
// decides whether the picture tells the truth. Everything below is pure — it
// takes numbers and geometry and returns numbers and SVG path strings.
import type { CurvePoint } from './types'

/**
 * Where every performance index starts, mirroring `indexBase` in
 * internal/db/curve.go: 10000 minor units read as 100.00.
 */
export const INDEX_BASE = 10000

/**
 * What the chart is plotting.
 *
 * These are two modes rather than two lines on one axis, and that is the whole
 * design. Money and a notional index around 100 do not share a scale: drawing
 * an index beside a market value in the millions would flatten it to the
 * baseline and imply a relationship that is not there.
 */
export type CurveMode = 'value' | 'performance'

/** The value range an axis spans. */
export interface Extent {
  min: number
  max: number
}

/** The drawing area inside the chart's padding, in SVG user units. */
export interface Plot {
  x: number
  y: number
  width: number
  height: number
}

export interface ChartGeometry {
  width: number
  height: number
  padding: { top: number; right: number; bottom: number; left: number }
}

export interface CurveSeries {
  key: 'market_value' | 'net_invested' | 'index'
  label: string
  values: number[]
}

export interface ChartLine extends CurveSeries {
  /** The SVG `d` attribute for this series. */
  path: string
}

export interface ChartTick {
  value: number
  y: number
}

export interface ChartLabel {
  label: string
  x: number
}

export interface ChartModel {
  lines: ChartLine[]
  ticks: ChartTick[]
  xLabels: ChartLabel[]
  extent: Extent
  plot: Plot
}

/** How much of the span to leave above the highest point, so it clears the frame. */
const HEADROOM = 0.08

/**
 * The value each mode's axis must always include.
 *
 * Money is anchored at 0 so a book wobbling between 1.00M and 1.01M is drawn as
 * the flat year it was rather than as a cliff. The index is anchored at its base
 * so "did this beat standing still?" is always a line on screen rather than a
 * number to work out.
 */
export function anchorFor(mode: CurveMode): number {
  return mode === 'value' ? 0 : INDEX_BASE
}

/**
 * The range to plot over: every value, plus the anchor, plus headroom on
 * whichever ends are not the anchor.
 *
 * The anchor is never padded away from the edge — it is a reference line the
 * reader measures against, so moving it would defeat the point of having one.
 */
export function extentOf(values: number[], anchor: number): Extent {
  let min = anchor
  let max = anchor
  for (const v of values) {
    if (v < min) min = v
    if (v > max) max = v
  }
  if (min === max) {
    // A single flat line exactly on the anchor: give it a band to sit in the
    // middle of rather than dividing by a zero span.
    const pad = Math.abs(min) * 0.1 || 1
    return { min: min - pad, max: max + pad }
  }
  const pad = (max - min) * HEADROOM
  return {
    min: min === anchor ? min : min - pad,
    max: max === anchor ? max : max + pad,
  }
}

/**
 * The horizontal position of the i-th of count sessions. A lone session sits in
 * the middle, there being no span to spread it across.
 */
export function scaleX(i: number, count: number, plot: Plot): number {
  if (count <= 1) return plot.x + plot.width / 2
  return plot.x + (plot.width * i) / (count - 1)
}

/** The vertical position of a value. SVG y grows downward, so the max is at the top. */
export function scaleY(value: number, extent: Extent, plot: Plot): number {
  const span = extent.max - extent.min
  if (span === 0) return plot.y + plot.height / 2
  return plot.y + plot.height * (1 - (value - extent.min) / span)
}

/**
 * The SVG path through a series.
 *
 * A single point is emitted as a zero-length line rather than a bare move, so a
 * round line cap renders it as a dot: one session is a real answer, and drawing
 * nothing for it would look like a failure to load.
 */
export function pathOf(values: number[], extent: Extent, plot: Plot): string {
  if (values.length === 0) return ''
  const at = (i: number) => `${scaleX(i, values.length, plot).toFixed(2)} ${scaleY(values[i], extent, plot).toFixed(2)}`
  if (values.length === 1) return `M ${at(0)} L ${at(0)}`
  return values.map((_, i) => `${i === 0 ? 'M' : 'L'} ${at(i)}`).join(' ')
}

/**
 * A round step near span/target, from the 1-2-5 series. Grid labels a reader can
 * hold in their head beat labels that merely divide the range evenly.
 */
export function niceStep(span: number, target: number): number {
  if (!(span > 0) || target <= 0) return 1
  const raw = span / target
  const magnitude = 10 ** Math.floor(Math.log10(raw))
  const normalized = raw / magnitude
  const step = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10
  return step * magnitude
}

/** The grid values inside an extent, on round steps. */
export function ticksOf(extent: Extent, target = 4): number[] {
  const step = niceStep(extent.max - extent.min, target)
  const first = Math.ceil(extent.min / step) * step
  const out: number[] = []
  // Indexed rather than accumulated, so a fractional step cannot drift.
  for (let i = 0; ; i++) {
    const value = first + i * step
    if (value > extent.max || out.length > 32) break
    out.push(value)
  }
  return out
}

/**
 * Up to `max` evenly spaced session dates, always including the first and last.
 * The dates are already plain YYYY-MM-DD strings, so no time zone enters here.
 */
export function xLabelsOf(dates: string[], plot: Plot, max = 5): ChartLabel[] {
  if (dates.length === 0) return []
  if (dates.length <= max) {
    return dates.map((label, i) => ({ label, x: scaleX(i, dates.length, plot) }))
  }
  const out: ChartLabel[] = []
  for (let n = 0; n < max; n++) {
    const i = Math.round((n * (dates.length - 1)) / (max - 1))
    out.push({ label: dates[i], x: scaleX(i, dates.length, plot) })
  }
  return out
}

/** The session nearest a pointer position, for the hover readout. -1 when empty. */
export function indexAtX(x: number, count: number, plot: Plot): number {
  if (count <= 0) return -1
  if (count === 1) return 0
  const fraction = plot.width === 0 ? 0 : (x - plot.x) / plot.width
  const i = Math.round(fraction * (count - 1))
  return Math.min(count - 1, Math.max(0, i))
}

/**
 * The series a mode plots.
 *
 * Value mode draws market value against money paid in. They share one scale
 * because they are the same unit and the gap between them *is* the gain — which
 * is only readable when both are measured the same way.
 */
export function seriesOf(points: CurvePoint[], mode: CurveMode): CurveSeries[] {
  if (mode === 'performance') {
    return [{ key: 'index', label: 'Index', values: points.map((p) => p.index) }]
  }
  return [
    { key: 'market_value', label: 'Market value', values: points.map((p) => p.market_value) },
    { key: 'net_invested', label: 'Money in', values: points.map((p) => p.net_invested) },
  ]
}

/**
 * The index as the notional 100 a reader is meant to see: 11000 -> "110.0".
 *
 * It is stored in minor units like every other amount here, but it is not money
 * and must never be formatted as any — the whole point of the index is that it
 * is comparable between books whose currencies are not.
 */
export function formatIndex(index: number): string {
  return (index / 100).toFixed(1)
}

/** The plotting area left inside a geometry's padding. */
export function plotOf(geometry: ChartGeometry): Plot {
  const { width, height, padding } = geometry
  return {
    x: padding.left,
    y: padding.top,
    width: Math.max(0, width - padding.left - padding.right),
    height: Math.max(0, height - padding.top - padding.bottom),
  }
}

/**
 * Everything the chart component needs to render one mode: the paths, the grid
 * and the axis labels, all on a single shared extent.
 */
export function buildCurveChart(
  points: CurvePoint[],
  mode: CurveMode,
  geometry: ChartGeometry,
): ChartModel {
  const plot = plotOf(geometry)
  const series = seriesOf(points, mode)
  // One extent across every series in the mode: two money lines on separate
  // scales would put the gain between them wherever the scaling happened to
  // land it.
  const extent = extentOf(series.flatMap((s) => s.values), anchorFor(mode))
  return {
    lines: series.map((s) => ({ ...s, path: pathOf(s.values, extent, plot) })),
    ticks: ticksOf(extent).map((value) => ({ value, y: scaleY(value, extent, plot) })),
    xLabels: xLabelsOf(points.map((p) => p.date), plot),
    extent,
    plot,
  }
}
