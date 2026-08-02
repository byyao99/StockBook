import { describe, expect, it } from 'vitest'
import {
  UNKNOWN,
  formatBpsOrUnknown,
  formatCents,
  formatCentsOrUnknown,
  formatPercent,
  formatPercentOrUnknown,
  formatQty,
  formatSignedCents,
  formatSignedOrUnknown,
  fromCents,
  toCents,
} from './money'

describe('formatCents', () => {
  it('formats whole dollars with two decimals', () => {
    expect(formatCents(18000)).toBe('$180.00')
  })

  it('formats sub-dollar amounts', () => {
    expect(formatCents(5)).toBe('$0.05')
    expect(formatCents(50)).toBe('$0.50')
  })

  it('formats zero', () => {
    expect(formatCents(0)).toBe('$0.00')
  })
})

describe('toCents', () => {
  it('converts dollars to integer cents', () => {
    expect(toCents(180)).toBe(18000)
    expect(toCents(9.99)).toBe(999)
  })

  it('rounds instead of truncating float artifacts', () => {
    // 19.99 * 100 is 1998.9999999999998 in IEEE-754; Math.round must fix it.
    expect(toCents(19.99)).toBe(1999)
    expect(toCents(0.1 + 0.2)).toBe(30)
  })
})

describe('fromCents', () => {
  it('converts cents back to a dollar number', () => {
    expect(fromCents(18000)).toBe(180)
    expect(fromCents(999)).toBe(9.99)
  })

  it('round-trips with toCents', () => {
    for (const cents of [0, 1, 99, 100, 1999, 123456]) {
      expect(toCents(fromCents(cents))).toBe(cents)
    }
  })
})

describe('formatSignedCents', () => {
  // A gain and a loss must never look alike at a glance, and the sign carries
  // that even where color is unavailable.
  it('always carries an explicit sign', () => {
    expect(formatSignedCents(2500)).toBe('+$25.00')
    expect(formatSignedCents(-2500)).toBe('-$25.00')
  })

  it('treats break-even as a non-loss', () => {
    expect(formatSignedCents(0)).toBe('+$0.00')
  })
})

describe('formatPercent', () => {
  it('formats a fraction as a signed percentage', () => {
    expect(formatPercent(0.1234)).toBe('+12.34%')
    expect(formatPercent(-0.5)).toBe('-50.00%')
    expect(formatPercent(0)).toBe('+0.00%')
  })
})

describe('formatQty', () => {
  it('groups thousands so large share counts stay readable', () => {
    expect(formatQty(1000)).toBe('1,000')
    expect(formatQty(12345678)).toBe('12,345,678')
    expect(formatQty(7)).toBe('7')
  })
})

describe('the unknown-value formatters', () => {
  // Showing an unvalued holding as $0.00 would read as a total loss, so every
  // nullable number gets an explicit "not known" rendering instead.
  it('render null as the unknown marker rather than zero', () => {
    expect(formatCentsOrUnknown(null)).toBe(UNKNOWN)
    expect(formatSignedOrUnknown(null)).toBe(UNKNOWN)
    expect(formatPercentOrUnknown(null)).toBe(UNKNOWN)
  })

  it('pass real values through, including zero', () => {
    expect(formatCentsOrUnknown(0)).toBe('$0.00')
    expect(formatCentsOrUnknown(18000)).toBe('$180.00')
    expect(formatSignedOrUnknown(-1)).toBe('-$0.01')
    expect(formatPercentOrUnknown(0.05)).toBe('+5.00%')
  })
})

describe('formatBpsOrUnknown', () => {
  it('reads basis points as a signed percentage', () => {
    expect(formatBpsOrUnknown(1234)).toBe('+12.34%')
    expect(formatBpsOrUnknown(-5000)).toBe('-50.00%')
    expect(formatBpsOrUnknown(10000)).toBe('+100.00%')
  })

  it('shows breaking even as zero, not as unknown', () => {
    expect(formatBpsOrUnknown(0)).toBe('+0.00%')
  })

  it('marks a rate that could not be computed as unknown', () => {
    expect(formatBpsOrUnknown(null)).toBe(UNKNOWN)
  })
})
