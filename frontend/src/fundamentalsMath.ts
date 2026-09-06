/**
 * The arithmetic behind the reported-figures panel.
 *
 * It lives apart from the component for the reason `curveMath.ts` does: scaling
 * and comparison are the parts worth testing, and testing them through a
 * rendered chart would mean a DOM for no gain.
 *
 * Every function here returns `null` rather than `0` when it cannot answer. A
 * quarter the provider has no figure for is unknown, not a quarter the company
 * earned nothing in, and the difference is the whole reason the API sends nulls.
 */
import type { FinancialMetric, FinancialPeriod } from './types'

// Month names for a period label. English, like every other string in this app.
const MONTHS = [
  'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun',
  'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec',
]

/** The value of one metric in one period, or null when it was not reported. */
export function valueOf(period: FinancialPeriod, metric: FinancialMetric): number | null {
  switch (metric) {
    case 'revenue':
      return period.revenue
    case 'net_income':
      return period.net_income
    case 'diluted_eps':
      return period.diluted_eps
  }
}

/**
 * One column of the figures chart.
 *
 * `height` is the fraction of the plot this bar fills, 0 to 1. A period with no
 * figure gets a height of 0 *and* a null value, which the view draws as a gap
 * rather than as a bar sitting on the axis — a missing quarter and a quarter at
 * zero must not look the same.
 */
export interface Bar {
  period: FinancialPeriod
  value: number | null
  height: number
  negative: boolean
}

/**
 * Scale a run of periods into bars.
 *
 * The scale is the largest **magnitude** in the run, not the largest value, so a
 * loss-making quarter is drawn to the same scale as a profitable one instead of
 * running off the bottom of the plot. Net income genuinely goes negative;
 * revenue does not, and the same rule serves both.
 *
 * A run with nothing in it, or nothing but nulls, produces bars of zero height
 * rather than dividing by zero.
 */
export function buildBars(periods: FinancialPeriod[], metric: FinancialMetric): Bar[] {
  const values = periods.map((period) => valueOf(period, metric))
  const scale = values.reduce<number>(
    (largest, value) => (value === null ? largest : Math.max(largest, Math.abs(value))),
    0,
  )
  return periods.map((period, index) => {
    const value = values[index]
    return {
      period,
      value,
      height: value === null || scale === 0 ? 0 : Math.abs(value) / scale,
      negative: value !== null && value < 0,
    }
  })
}

/**
 * Name a reporting period.
 *
 * A quarter is labelled by the month it **ended** — "Jun 2026" — rather than by
 * a quarter number, because the number is the company's own and cannot be
 * derived from the date: Apple's quarter ending in September is its fourth, not
 * the calendar's third. The end month is a fact; "Q3" would be a guess.
 *
 * The date is sliced out of the string rather than parsed into a Date, so the
 * label does not shift with the reader's time zone — the same rule the ledger
 * follows.
 */
export function periodLabel(period: FinancialPeriod): string {
  const year = period.as_of_date.slice(0, 4)
  if (period.period_type !== '3M') return year
  const month = Number(period.as_of_date.slice(5, 7))
  if (!Number.isInteger(month) || month < 1 || month > 12) return period.as_of_date
  return `${MONTHS[month - 1]} ${year}`
}

/**
 * The change in one metric against the same period a year earlier, as a
 * fraction: 0.25 is a 25% rise.
 *
 * The comparison is found by date — the period ending in the same month one year
 * back — rather than by counting four rows back, because a run with a gap in it
 * would otherwise compare a quarter against the wrong one and report the
 * difference as growth.
 *
 * The base is divided by its **magnitude**, so a company swinging from a loss of
 * 100 to a profit of 50 reads as +150% rather than -150%. A base of zero yields
 * null: there is no growth rate from nothing, and a division would say Infinity.
 */
export function yoyChange(
  periods: FinancialPeriod[],
  metric: FinancialMetric,
  index: number,
): number | null {
  const current = periods[index]
  if (!current) return null
  const value = valueOf(current, metric)
  if (value === null) return null

  const prior = priorYear(periods, current)
  if (!prior) return null
  const base = valueOf(prior, metric)
  if (base === null || base === 0) return null

  return (value - base) / Math.abs(base)
}

/** The period ending in the same month one year before the given one. */
function priorYear(
  periods: FinancialPeriod[],
  current: FinancialPeriod,
): FinancialPeriod | undefined {
  const year = Number(current.as_of_date.slice(0, 4))
  const month = current.as_of_date.slice(5, 7)
  if (!Number.isInteger(year)) return undefined
  return periods.find(
    (period) =>
      period.period_type === current.period_type &&
      Number(period.as_of_date.slice(0, 4)) === year - 1 &&
      period.as_of_date.slice(5, 7) === month,
  )
}

/**
 * The most recent period that actually carries a figure for the metric.
 *
 * The headline number on the panel comes from here rather than from the last row
 * outright, so a company whose newest quarter is still missing its EPS shows the
 * one before it instead of an em dash where a figure clearly exists.
 */
export function latestReported(
  periods: FinancialPeriod[],
  metric: FinancialMetric,
): FinancialPeriod | null {
  for (let index = periods.length - 1; index >= 0; index -= 1) {
    if (valueOf(periods[index], metric) !== null) return periods[index]
  }
  return null
}
