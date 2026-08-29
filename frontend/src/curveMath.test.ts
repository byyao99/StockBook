import { describe, expect, it } from 'vitest'
import {
  INDEX_BASE,
  anchorFor,
  buildCurveChart,
  extentOf,
  formatIndex,
  indexAtX,
  niceStep,
  pathOf,
  plotOf,
  scaleX,
  scaleY,
  seriesOf,
  ticksOf,
  xLabelsOf,
} from './curveMath'
import type { ChartGeometry, Plot } from './curveMath'
import type { CurvePoint } from './types'

// A plain 100x100 area at the origin, so a position reads as a percentage.
const plot: Plot = { x: 0, y: 0, width: 100, height: 100 }

const geometry: ChartGeometry = {
  width: 120,
  height: 120,
  padding: { top: 10, right: 10, bottom: 10, left: 10 },
}

function point(overrides: Partial<CurvePoint> = {}): CurvePoint {
  return {
    date: '2026-03-02',
    market_value: 1_000_000,
    net_invested: 1_000_000,
    index: INDEX_BASE,
    ...overrides,
  }
}

describe('anchorFor', () => {
  it('anchors money at zero', () => {
    expect(anchorFor('value')).toBe(0)
  })

  it('anchors performance at the index base, so "flat" is always on screen', () => {
    expect(anchorFor('performance')).toBe(INDEX_BASE)
  })
})

describe('extentOf', () => {
  it('always includes the anchor, even when every value is far above it', () => {
    // The whole point of anchoring money at 0: a book between 1.00M and 1.01M
    // is a flat year, and an extent hugging the values would draw it as a cliff.
    const { min } = extentOf([1_000_000, 1_010_000], 0)
    expect(min).toBe(0)
  })

  it('never pads the anchor away from the edge', () => {
    expect(extentOf([50, 100], 0).min).toBe(0)
    expect(extentOf([9_000, 11_000], INDEX_BASE).max).toBeGreaterThan(11_000)
  })

  it('leaves headroom above the highest value so it clears the frame', () => {
    expect(extentOf([100], 0).max).toBeGreaterThan(100)
  })

  it('pads below the lowest value when the anchor is not the floor', () => {
    // An index that only ever fell: the base is the ceiling here, so the low end
    // is the one that needs room.
    const { min, max } = extentOf([9_000, 9_500], INDEX_BASE)
    expect(min).toBeLessThan(9_000)
    expect(max).toBe(INDEX_BASE)
  })

  it('gives a perfectly flat line a band rather than a zero span', () => {
    const { min, max } = extentOf([INDEX_BASE], INDEX_BASE)
    expect(max).toBeGreaterThan(min)
    expect(min).toBeLessThan(INDEX_BASE)
    expect(max).toBeGreaterThan(INDEX_BASE)
  })

  it('handles an empty series by falling back to the anchor alone', () => {
    const { min, max } = extentOf([], 0)
    expect(max).toBeGreaterThan(min)
  })
})

describe('scaleX', () => {
  it('spreads sessions from the left edge to the right', () => {
    expect(scaleX(0, 3, plot)).toBe(0)
    expect(scaleX(1, 3, plot)).toBe(50)
    expect(scaleX(2, 3, plot)).toBe(100)
  })

  it('centres a lone session, there being no span to spread it over', () => {
    expect(scaleX(0, 1, plot)).toBe(50)
  })
})

describe('scaleY', () => {
  it('puts the maximum at the top, because SVG y grows downward', () => {
    expect(scaleY(100, { min: 0, max: 100 }, plot)).toBe(0)
    expect(scaleY(0, { min: 0, max: 100 }, plot)).toBe(100)
    expect(scaleY(50, { min: 0, max: 100 }, plot)).toBe(50)
  })

  it('centres a value when the extent has no span rather than dividing by zero', () => {
    expect(scaleY(5, { min: 5, max: 5 }, plot)).toBe(50)
  })
})

describe('pathOf', () => {
  it('moves to the first point and lines to the rest', () => {
    expect(pathOf([0, 100], { min: 0, max: 100 }, plot)).toBe('M 0.00 100.00 L 100.00 0.00')
  })

  it('draws a single session as a zero-length line, which a round cap renders as a dot', () => {
    // One session is a real answer; drawing nothing for it would look like a
    // chart that failed to load.
    expect(pathOf([50], { min: 0, max: 100 }, plot)).toBe('M 50.00 50.00 L 50.00 50.00')
  })

  it('is empty for no points at all', () => {
    expect(pathOf([], { min: 0, max: 100 }, plot)).toBe('')
  })
})

describe('niceStep', () => {
  it('picks a round step from the 1-2-5 series', () => {
    expect(niceStep(100, 4)).toBe(50) // 100/4 = 25, rounded up the series to 50
    expect(niceStep(10, 5)).toBe(2)
    expect(niceStep(1000, 4)).toBe(500)
  })

  it('falls back to 1 for a degenerate span rather than returning 0 or NaN', () => {
    expect(niceStep(0, 4)).toBe(1)
    expect(niceStep(-5, 4)).toBe(1)
    expect(niceStep(100, 0)).toBe(1)
  })
})

describe('ticksOf', () => {
  it('lands on round values inside the extent', () => {
    expect(ticksOf({ min: 0, max: 100 }, 4)).toEqual([0, 50, 100])
  })

  it('never emits a tick outside the extent it was given', () => {
    const extent = { min: 17, max: 93 }
    for (const t of ticksOf(extent)) {
      expect(t).toBeGreaterThanOrEqual(extent.min)
      expect(t).toBeLessThanOrEqual(extent.max)
    }
  })

  it('terminates on a zero span instead of looping forever', () => {
    expect(ticksOf({ min: 5, max: 5 }).length).toBeLessThanOrEqual(32)
  })
})

describe('xLabelsOf', () => {
  it('labels every session when there are few enough', () => {
    expect(xLabelsOf(['2026-01-01', '2026-01-02'], plot).map((l) => l.label)).toEqual([
      '2026-01-01',
      '2026-01-02',
    ])
  })

  it('thins a long series down but always keeps the first and last', () => {
    const dates = Array.from({ length: 400 }, (_, i) => `day-${i}`)
    const labels = xLabelsOf(dates, plot, 5)
    expect(labels).toHaveLength(5)
    expect(labels[0].label).toBe('day-0')
    expect(labels[4].label).toBe('day-399')
  })

  it('has nothing to label for an empty curve', () => {
    expect(xLabelsOf([], plot)).toEqual([])
  })
})

describe('indexAtX', () => {
  it('snaps to the nearest session', () => {
    expect(indexAtX(0, 3, plot)).toBe(0)
    expect(indexAtX(49, 3, plot)).toBe(1)
    expect(indexAtX(100, 3, plot)).toBe(2)
  })

  it('clamps a pointer that wandered outside the plot', () => {
    expect(indexAtX(-40, 3, plot)).toBe(0)
    expect(indexAtX(999, 3, plot)).toBe(2)
  })

  it('reports -1 when there is nothing to hover over', () => {
    expect(indexAtX(50, 0, plot)).toBe(-1)
  })
})

describe('seriesOf', () => {
  it('plots market value against money in, on the value mode', () => {
    const lines = seriesOf([point()], 'value')
    expect(lines.map((l) => l.key)).toEqual(['market_value', 'net_invested'])
  })

  it('plots the index alone on the performance mode', () => {
    expect(seriesOf([point()], 'performance').map((l) => l.key)).toEqual(['index'])
  })
})

describe('formatIndex', () => {
  it('reads the stored minor units back as the notional 100', () => {
    expect(formatIndex(INDEX_BASE)).toBe('100.0')
    expect(formatIndex(11_000)).toBe('110.0')
    expect(formatIndex(8_425)).toBe('84.3')
  })
})

describe('plotOf', () => {
  it('is the geometry inside its padding', () => {
    expect(plotOf(geometry)).toEqual({ x: 10, y: 10, width: 100, height: 100 })
  })
})

describe('buildCurveChart', () => {
  const points = [
    point({ date: '2026-03-02', market_value: 1_000_000, net_invested: 1_000_000, index: 10_000 }),
    point({ date: '2026-03-03', market_value: 1_100_000, net_invested: 1_000_000, index: 11_000 }),
  ]

  it('gives both money lines one shared extent, so the gap between them is the gain', () => {
    // Scaling them separately would put the gap wherever each line's own range
    // happened to land it, which is exactly the number a reader is looking for.
    const chart = buildCurveChart(points, 'value', geometry)
    expect(chart.lines).toHaveLength(2)
    expect(chart.extent.min).toBe(0)
    expect(chart.extent.max).toBeGreaterThanOrEqual(1_100_000)
  })

  it('keeps the index base inside the performance extent', () => {
    // Without this the reader loses the only line that says "went nowhere".
    const chart = buildCurveChart(points, 'performance', geometry)
    expect(chart.lines).toHaveLength(1)
    expect(chart.extent.min).toBeLessThanOrEqual(INDEX_BASE)
    expect(chart.extent.max).toBeGreaterThanOrEqual(INDEX_BASE)
  })

  it('never plots a market value on the same scale as an index', () => {
    // The two modes exist because these do not belong on one axis: an index
    // around 100 drawn against millions would sit flat on the baseline.
    const value = buildCurveChart(points, 'value', geometry)
    const performance = buildCurveChart(points, 'performance', geometry)
    expect(performance.extent.max).toBeLessThan(value.extent.max)
  })

  it('survives an empty curve without throwing', () => {
    const chart = buildCurveChart([], 'value', geometry)
    expect(chart.lines.every((l) => l.path === '')).toBe(true)
    expect(chart.xLabels).toEqual([])
  })
})
