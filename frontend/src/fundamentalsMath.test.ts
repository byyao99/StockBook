import { describe, expect, it } from 'vitest'
import { buildBars, latestReported, periodLabel, valueOf, yoyChange } from './fundamentalsMath'
import type { FinancialPeriod } from './types'

// A quarter, with only the fields a case cares about spelled out.
function quarter(asOf: string, overrides: Partial<FinancialPeriod> = {}): FinancialPeriod {
  return {
    as_of_date: asOf,
    period_type: '3M',
    currency: 'TWD',
    revenue: null,
    net_income: null,
    diluted_eps: null,
    ...overrides,
  }
}

describe('buildBars', () => {
  // The tallest bar fills the plot and the rest are measured against it.
  it('scales every bar against the largest figure in the run', () => {
    const bars = buildBars(
      [quarter('2026-03-31', { revenue: 500 }), quarter('2026-06-30', { revenue: 1000 })],
      'revenue',
    )
    expect(bars[0].height).toBe(0.5)
    expect(bars[1].height).toBe(1)
  })

  // Net income genuinely goes negative. Scaling on the largest magnitude keeps a
  // loss-making quarter on the same scale as a profitable one instead of running
  // off the bottom of the plot.
  it('scales on magnitude so a loss is drawn to scale', () => {
    const bars = buildBars(
      [quarter('2026-03-31', { net_income: -800 }), quarter('2026-06-30', { net_income: 400 })],
      'net_income',
    )
    expect(bars[0].height).toBe(1)
    expect(bars[0].negative).toBe(true)
    expect(bars[1].height).toBe(0.5)
    expect(bars[1].negative).toBe(false)
  })

  // A quarter with no figure is a gap, not a bar sitting on the axis: unknown
  // and zero must not look the same.
  it('gives an unreported period a null value, not a zero bar', () => {
    const bars = buildBars(
      [quarter('2026-03-31'), quarter('2026-06-30', { revenue: 1000 })],
      'revenue',
    )
    expect(bars[0].value).toBeNull()
    expect(bars[0].height).toBe(0)
  })

  // Nothing to scale against must not divide by zero.
  it('produces flat bars rather than dividing by zero', () => {
    const bars = buildBars([quarter('2026-03-31'), quarter('2026-06-30')], 'revenue')
    expect(bars.every((bar) => bar.height === 0)).toBe(true)
  })
})

describe('periodLabel', () => {
  // A quarter number is the company's own and cannot be derived from the date:
  // Apple's quarter ending in September is its fourth, not the calendar's third.
  // The end month is a fact.
  it('names a quarter by the month it ended, never by a quarter number', () => {
    expect(periodLabel(quarter('2026-06-30'))).toBe('Jun 2026')
    expect(periodLabel(quarter('2024-09-30'))).toBe('Sep 2024')
  })

  it('names an annual period by its year', () => {
    expect(periodLabel(quarter('2024-09-30', { period_type: '12M' }))).toBe('2024')
  })
})

describe('yoyChange', () => {
  // 1,270 against 933 a year earlier is a rise of 337, or 36.1%.
  it('compares against the period ending in the same month a year back', () => {
    const periods = [
      quarter('2025-06-30', { revenue: 933 }),
      quarter('2025-09-30', { revenue: 989 }),
      quarter('2026-06-30', { revenue: 1270 }),
    ]
    expect(yoyChange(periods, 'revenue', 2)).toBeCloseTo((1270 - 933) / 933, 10)
  })

  // Found by date, not by counting four rows back: a run with a gap in it would
  // otherwise compare a quarter against the wrong one and call the difference
  // growth.
  it('is null when the matching quarter is missing rather than comparing the wrong one', () => {
    const periods = [
      quarter('2025-03-31', { revenue: 100 }),
      quarter('2026-06-30', { revenue: 200 }),
    ]
    expect(yoyChange(periods, 'revenue', 1)).toBeNull()
  })

  // A swing from a loss of 100 to a profit of 50 is a gain of 150, and dividing
  // by the magnitude is what stops it reading as -150%.
  it('measures a swing out of a loss as a gain', () => {
    const periods = [
      quarter('2025-06-30', { net_income: -100 }),
      quarter('2026-06-30', { net_income: 50 }),
    ]
    expect(yoyChange(periods, 'net_income', 1)).toBeCloseTo(1.5, 10)
  })

  // There is no growth rate from nothing, and the division would say Infinity.
  it('is null against a base of zero', () => {
    const periods = [
      quarter('2025-06-30', { revenue: 0 }),
      quarter('2026-06-30', { revenue: 200 }),
    ]
    expect(yoyChange(periods, 'revenue', 1)).toBeNull()
  })

  it('is null when either period has no figure', () => {
    const periods = [quarter('2025-06-30'), quarter('2026-06-30', { revenue: 200 })]
    expect(yoyChange(periods, 'revenue', 1)).toBeNull()
    expect(yoyChange(periods, 'revenue', 0)).toBeNull()
  })
})

describe('latestReported', () => {
  // A company whose newest quarter is still missing its EPS shows the one before
  // it, rather than an em dash where a figure clearly exists.
  it('skips back past a period the metric was not reported in', () => {
    const periods = [
      quarter('2026-03-31', { diluted_eps: 1500 }),
      quarter('2026-06-30', { revenue: 1270 }),
    ]
    expect(latestReported(periods, 'diluted_eps')?.as_of_date).toBe('2026-03-31')
    expect(latestReported(periods, 'revenue')?.as_of_date).toBe('2026-06-30')
  })

  it('is null when the metric was never reported', () => {
    expect(latestReported([quarter('2026-06-30')], 'revenue')).toBeNull()
  })
})

describe('valueOf', () => {
  it('reads each metric off its own field', () => {
    const period = quarter('2026-06-30', { revenue: 1, net_income: 2, diluted_eps: 3 })
    expect(valueOf(period, 'revenue')).toBe(1)
    expect(valueOf(period, 'net_income')).toBe(2)
    expect(valueOf(period, 'diluted_eps')).toBe(3)
  })
})
